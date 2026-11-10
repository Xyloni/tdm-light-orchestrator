package orchestrator

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed panel.html
var panelHTML string

var page = template.Must(template.New("panel").Parse(panelHTML))

type panelGame struct {
	Mode, PolicyLabel, Report, Quarantine       string
	ReserveMinutes                              int
	Key, Name, Errors, GroupSummary             string
	ExcludedRewards                             []string
	Coverage                                    []coverageCampaign
	CoverageSets                                []coverageSet
	Accounts, Target, Deficit                   int
	Active, Removing, Paused, Stopping, Regroup bool
}

type notificationView struct {
	Key, Label string
	Enabled    bool
}

type panelData struct {
	CampaignExport                            *webCampaignExport
	Lagging                                   laggingReport
	LaggingText                               string
	DigestAt                                  string
	Lifetime                                  totals
	StatsText                                 string
	Quarantine                                string
	Notifications                             []notificationView
	NotificationsMuted                        bool
	HelpTopics                                []helpTopic
	Categories                                []string
	SearchQuery                               string
	SearchTarget                              int
	Token, Status, Message, CatalogStatus     string
	Settings                                  settings
	Options                                   minerOptions
	Games                                     []panelGame
	ActiveGames, ActiveAccounts, FreeAccounts int
	ShowConfirm                               bool
	BotPlatform, BotOwnerID                   string
	BotTokenSaved, LegacyBots                 bool
}

type webCampaignExport struct {
	Game, Name, Mode, ModeLabel, Fingerprint string
	IDs                                      []string
	Names                                    []string
	Count                                    int
}

func (s *scheduler) prepareWebCampaignExport(form url.Values) (webCampaignExport, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := form.Get("game")
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return webCampaignExport{}, nil, fmt.Errorf("игра недоступна")
	}
	ids, mode := form["campaign"], form.Get("mode")
	accounts, err := s.coverage(key, g).selectAccounts(ids, mode)
	if err != nil {
		return webCampaignExport{}, nil, err
	}
	if len(accounts) == 0 {
		return webCampaignExport{}, nil, fmt.Errorf("по фильтру нет аккаунтов")
	}
	data, err := encodeAccountFile(accounts)
	if err != nil {
		return webCampaignExport{}, nil, err
	}
	names := make([]string, 0, len(ids))
	for _, c := range g.Campaigns {
		for _, id := range ids {
			if c.ID == id {
				names = append(names, c.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return webCampaignExport{Game: key, Name: g.Name, Mode: mode, ModeLabel: campaignExportModeLabel(mode),
		IDs: ids, Names: names, Count: len(accounts), Fingerprint: campaignExportFingerprint(data)}, data, nil
}

func (s *scheduler) panel(token, message string, showConfirm bool) panelData {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int)
	for _, a := range s.state.Accounts {
		counts[a.Game]++
	}
	view := panelData{
		Token: token, Message: message, Status: s.status(), CatalogStatus: s.catalogSummary(), HelpTopics: helpTopics, Settings: s.state.Settings, Options: s.minerOptions(),
		ActiveGames: len(s.active), ActiveAccounts: s.runningAccounts(), FreeAccounts: counts[""],
		ShowConfirm: showConfirm && len(s.trimPreview) > 0 && time.Now().Before(s.trimUntil) && s.runningAccounts() > s.state.Settings.MaxAccounts,
	}
	view.NotificationsMuted = s.state.NotificationsMuted
	for _, item := range noticeKinds {
		view.Notifications = append(view.Notifications, notificationView{item.Key, item.Label, s.noticeConfigured(item.Key)})
	}
	view.DigestAt = s.state.Digest.At
	view.Lagging = s.laggingReport("", time.Now())
	view.LaggingText = view.Lagging.text()
	view.Lifetime = s.state.Stats.sum("", "")
	view.StatsText = s.statsText("", time.Now())
	view.Quarantine = s.quarantineSummary("")
	view.BotPlatform = "none"
	if bots := s.configuredBots(); len(bots) > 0 {
		view.BotPlatform, view.BotOwnerID = bots[0].Platform, bots[0].OwnerID
		view.BotTokenSaved = bots[0].Token != ""
		view.LegacyBots = len(bots) > 1
	}
	now := time.Now()
	for key, g := range s.state.Games {
		groupSummary := s.queueSummary(key, g, now)
		coverage := s.coverage(key, g)
		a := s.active[key]
		stopping := a != nil && a.stopping
		policy := s.gameSettings(g)
		view.Games = append(view.Games, panelGame{
			Mode: g.Mode, ReserveMinutes: g.ReserveMinutes, PolicyLabel: policy.modeName() + " · запас " + strconv.Itoa(policy.ReserveMinutes) + " мин",
			Report: s.campaignReport(g), Quarantine: s.quarantineSummary(key),
			Coverage: coverage.Campaigns, CoverageSets: coverage.Sets,
			Key: key, Name: g.Name, Accounts: counts[key], Target: g.Target, Deficit: max(0, g.Target-counts[key]),
			Active: a != nil, Removing: g.Removing, Paused: g.Paused, Stopping: stopping, Regroup: g.Regroup, Errors: s.accountErrors(key), GroupSummary: groupSummary, ExcludedRewards: append([]string(nil), g.ExcludedRewards...),
		})
	}
	sort.Slice(view.Games, func(i, j int) bool { return view.Games[i].Key < view.Games[j].Key })
	return view
}

func (s *scheduler) panelAction(form url.Values) string {
	action := form.Get("action")
	game := strings.TrimSpace(form.Get("game"))
	switch action {
	case "add", "target":
		target, err := strconv.Atoi(form.Get("target"))
		if err != nil || target < 1 || game == "" {
			return "Укажите название игры и положительный размер пула."
		}
		return s.operate(action, strconv.Itoa(target)+" "+game)
	case "stop", "resume", "restart":
		return s.operate(action, game)
	case "exclude", "include":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.changeExclusion(strings.ToLower(game), form.Get("reward"), action == "exclude")
	case "remove":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.requestGameRemoval(strings.ToLower(game), form.Get("disposition"), form.Get("confirm") == "on")
	case "game-policy":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.changeGamePolicy(strings.ToLower(game), form.Get("mode"), form.Get("reserve"))
	case "preview":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.previewSchedule(form.Get("scope"), form.Get("mode"), time.Now())
	case "simulate":
		return simulateInput(form.Get("scenario"))
	case "digest":
		return s.operate("digest", form.Get("at"))
	case "notifications":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.notificationSetting(form.Get("kind"), form.Get("enabled"))
	case "regroup":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.requestRegroup(form.Get("scope"), form.Get("confirm") == "on")
	case "mode":
		if form.Get("save_resources") == "on" {
			return s.operate("mode", "resources")
		}
		return s.operate("mode", "speed")
	case "limits", "timing":
		fields := []string{"max_games", "max_accounts"}
		if action == "timing" {
			fields = []string{"reserve", "rate", "interval"}
		}
		values := make([]string, 0, len(fields))
		for _, field := range fields {
			n, err := strconv.Atoi(form.Get(field))
			if err != nil || n < 1 {
				return "Проверьте числовые значения формы."
			}
			values = append(values, strconv.Itoa(n))
		}
		if action == "timing" {
			reserve, _ := strconv.Atoi(values[0])
			rate, _ := strconv.Atoi(values[1])
			interval, _ := strconv.Atoi(values[2])
			if reserve > 1440 || rate > 100 || interval < 15 || interval > 86400 {
				return "Допустимые значения: запас 1–1440 мин, скорость 1–100%, интервал 15–86400 сек."
			}
		}
		return s.operate(action, strings.Join(values, " "))
	case "bot":
		return s.saveBot(form.Get("platform"), form.Get("owner_id"), form.Get("bot_token"))
	case "miner":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.saveMinerOptions(minerOptions{
			Diagnostics: form.Get("diagnostics") == "on",
			SingleLog:   form.Get("single_log") == "on",
			UsePubSub:   form.Get("pubsub") == "on",
		})
	case "clear-pool":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.clearPool(form.Get("scope"), form.Get("disposition"), form.Get("confirm") == "on")
	case "move":
		login := strings.TrimSpace(form.Get("login"))
		if login == "" || strings.ContainsAny(login, " \r\n\t") {
			return "Укажите логин аккаунта."
		}
		if game == "" {
			return s.operate("free", login)
		}
		return s.operate("assign", login+" "+game)
	case "trim":
		return s.operate("trim", "")
	case "trim-confirm":
		return s.operate("trim", "confirm")
	default:
		return "Действие не распознано."
	}
}

func webHandler(s *scheduler) (http.Handler, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "Local access only", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		message := ""
		showConfirm := false
		var categories []string
		var searchQuery string
		var searchTarget int
		var campaignExport *webCampaignExport
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, (2<<20)+8192)
			multipart := strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")
			if multipart {
				err = r.ParseMultipartForm(2 << 20)
			} else {
				err = r.ParseForm()
			}
			if err != nil {
				http.Error(w, "Invalid form", http.StatusBadRequest)
				return
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(token)) != 1 {
				http.Error(w, "Invalid CSRF token", http.StatusForbidden)
				return
			}
			action := r.PostForm.Get("action")
			if action == "campaign-export" || action == "campaign-export-confirm" {
				preview, data, err := s.prepareWebCampaignExport(r.PostForm)
				switch {
				case err != nil:
					message = err.Error()
				case action == "campaign-export":
					campaignExport = &preview
				case subtle.ConstantTimeCompare([]byte(r.PostForm.Get("fingerprint")), []byte(preview.Fingerprint)) != 1:
					message = "Состав выборки изменился. Проверьте количество перед повторным подтверждением."
					campaignExport = &preview
				default:
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.Header().Set("Content-Disposition", `attachment; filename="accounts-campaigns.txt"`)
					w.Header().Set("Cache-Control", "no-store")
					w.Header().Set("X-Content-Type-Options", "nosniff")
					_, _ = w.Write(data)
					return
				}
			} else if action == "upload" && multipart {
				file, _, err := r.FormFile("accounts")
				if err != nil {
					http.Error(w, "Expected account file", http.StatusBadRequest)
					return
				}
				data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
				file.Close()
				if err != nil || len(data) > 2<<20 {
					http.Error(w, "Invalid account file", http.StatusBadRequest)
					return
				}
				message = "Аккаунты сохранены в выбранном пуле."
				if err = s.uploadToPool(data, r.PostForm.Get("game")); err != nil {
					message = err.Error()
				}
			} else if action == "search" {
				searchQuery = strings.TrimSpace(r.PostForm.Get("game"))
				searchTarget, _ = strconv.Atoi(r.PostForm.Get("target"))
				if len(searchQuery) < 2 || len(searchQuery) > 200 || searchTarget < 1 {
					message = "Введите название категории и положительный размер пула."
				} else {
					categories, err = s.searchCategories(r.Context(), searchQuery)
					if err != nil {
						message = err.Error()
					} else if len(categories) == 0 {
						message = "Категории не найдены. Уточните название."
					}
				}
			} else {
				message = s.panelAction(r.PostForm)
				showConfirm = action == "trim" || action == "trim-confirm"
			}
		} else if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		view := s.panel(token, message, showConfirm)
		view.CampaignExport = campaignExport
		view.Categories, view.SearchQuery, view.SearchTarget = categories, searchQuery, searchTarget
		_ = page.Execute(w, view)
	}), nil
}
