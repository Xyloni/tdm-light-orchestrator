package twitch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

const gqlURL = "https://gql.twitch.tv/gql"

var ErrNoDropsStreams = errors.New("в категории нет эфиров с Drops")
var ErrStreamOffline = errors.New("канал не в эфире")

// These Android app credentials match the OAuth tokens imported from
// TwitchDropsMiner/GrubDrops account.txt. Using the web Client-ID with
// Android-issued tokens causes Twitch's GQL integrity check to reject calls.
const androidUserAgent = "Dalvik/2.1.0 (Linux; U; Android 16; SM-S911B Build/TP1A.220624.014) tv.twitch.android.app/25.3.0/2503006"

// GQLClient — тонкий HTTP-клиент Twitch GraphQL для одного аккаунта.
// Никаких браузеров и медиа: только JSON POST на gql.twitch.tv.
type GQLClient struct {
	http      *http.Client
	cfg       *config.Config
	Account   *parser.Account
	segments  map[string]struct{} // HLS owner only; last successfully checked window
	spadeMu   sync.Mutex
	spadeURLs map[string]string
}

func NewGQLClient(h *http.Client, cfg *config.Config, acc *parser.Account) *GQLClient {
	if cfg.Diagnostics {
		clone := *h
		base := clone.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		clone.Transport = &diagnosticTransport{base: base, account: acc.Login}
		h = &clone
	}
	return &GQLClient{http: h, cfg: cfg, Account: acc, spadeURLs: make(map[string]string)}
}

// do выполняет один GQL-запрос с заголовками аккаунта:
// Authorization: OAuth <token>, Client-ID, X-Device-Id.
func (c *GQLClient) do(ctx context.Context, operation, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{
		"operationName": operation,
		"query":         query,
		"variables":     variables,
	})
	if err != nil {
		return err
	}
	return c.doPayload(ctx, operation, payload, out)
}

func (c *GQLClient) doPayload(ctx context.Context, operation string, payload []byte, out any) error {
	if c.cfg.Diagnostics {
		ctx = context.WithValue(ctx, diagnosticOperationKey{}, operation)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gqlURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Client-Id", c.cfg.ClientID)
	req.Header.Set("Authorization", "OAuth "+c.Account.OAuth)
	req.Header.Set("X-Device-Id", c.Account.DeviceID)
	req.Header.Set("User-Agent", androidUserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GQL %s: HTTP %d: %.200s", operation, resp.StatusCode, body)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"Message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("GQL %s: невалидный JSON: %w", operation, err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("GQL %s: %s", operation, env.Errors[0].Message)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("GQL %s: пустой ответ", operation)
	}
	return json.Unmarshal(env.Data, out)
}

// Drop — один time-based дроп кампании с прогрессом текущего аккаунта.
type Drop struct {
	ID             string
	Name           string
	CampaignID     string
	CampaignName   string
	CampaignStatus string
	GameName       string
	Required       int // требуемые минуты
	Current        int // нафармленные минуты
	IsClaimed      bool
	DropInstanceID string // появляется, когда дроп готов к клейму
	ProgressKnown  bool   // личные минуты подтверждены Inventory, а не каталогом
}

// DropsDashboard merges campaign discovery with account-specific Inventory.
// The catalog's self fields can be empty/stale even while watch time accrues.
// Both selections use one request, keeping polling cost bounded per account.
type Campaign struct {
	ID, Name, Status string
	StartAt, EndAt   time.Time
	Game             struct{ Name string }
	TimeBasedDrops   []struct {
		ID       string
		Name     string
		Required int `json:"requiredMinutesWatched"`
	}
}

func (c *GQLClient) Campaigns(ctx context.Context) ([]Campaign, error) {
	const query = `query OrchestratorCatalog {
		currentUser { dropCampaigns {
			id name status startAt endAt game { name }
			timeBasedDrops { id name requiredMinutesWatched }
		} }
	}`
	var data struct {
		CurrentUser *struct{ DropCampaigns []Campaign }
	}
	if err := c.do(ctx, "OrchestratorCatalog", query, nil, &data); err != nil {
		return nil, err
	}
	if data.CurrentUser == nil {
		return nil, fmt.Errorf("Twitch: отсутствует авторизованный пользователь")
	}
	return data.CurrentUser.DropCampaigns, nil
}

func (c *GQLClient) DropsDashboard(ctx context.Context) ([]Drop, error) {
	const query = `query Inventory {
  currentUser {
    dropCampaigns {
      id
      name
      status
      game { name }
      timeBasedDrops {
        id
        name
        requiredMinutesWatched
        self { isClaimed }
      }
    }
    inventory {
      dropCampaignsInProgress {
        id
        name
        status
        game { name }
        timeBasedDrops {
          id
          name
          requiredMinutesWatched
        self { currentMinutesWatched isClaimed dropInstanceID }
        }
      }
    }
  }
}`
	type campaign struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Game   *struct {
			Name string `json:"name"`
		} `json:"game"`
		TimeBasedDrops []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Required int    `json:"requiredMinutesWatched"`
			Self     *struct {
				Current    *int   `json:"currentMinutesWatched"`
				Claimed    bool   `json:"isClaimed"`
				InstanceID string `json:"dropInstanceID"`
			} `json:"self"`
		} `json:"timeBasedDrops"`
	}
	var data struct {
		CurrentUser *struct {
			DropCampaigns []campaign `json:"dropCampaigns"`
			Inventory     *struct {
				Campaigns []campaign `json:"dropCampaignsInProgress"`
			} `json:"inventory"`
		} `json:"currentUser"`
	}
	if err := c.do(ctx, "Inventory", query, nil, &data); err != nil {
		return nil, err
	}
	if data.CurrentUser == nil {
		return nil, fmt.Errorf("авторизация не удалась (currentUser == null) — проверьте oauth_token")
	}
	if data.CurrentUser.Inventory == nil {
		return nil, fmt.Errorf("Inventory: Twitch не вернул инвентарь; личный прогресс неизвестен")
	}
	var out []Drop
	type dropKey struct{ campaign, drop string }
	positions := make(map[dropKey]int)
	appendCampaign := func(camp campaign, personal bool) {
		gameName := ""
		if camp.Game != nil {
			gameName = camp.Game.Name
		}
		for _, d := range camp.TimeBasedDrops {
			drop := Drop{
				ID:             d.ID,
				Name:           d.Name,
				CampaignID:     camp.ID,
				CampaignName:   camp.Name,
				CampaignStatus: camp.Status,
				GameName:       gameName,
				Required:       d.Required,
			}
			if d.Self != nil {
				drop.IsClaimed = d.Self.Claimed
				if personal && d.Self.Current != nil {
					drop.Current = *d.Self.Current
					drop.DropInstanceID = d.Self.InstanceID
					drop.ProgressKnown = true
				}
			}
			key := dropKey{camp.ID, d.ID}
			if index, exists := positions[key]; exists {
				// Inventory is authoritative for personal progress. Retain catalog
				// metadata when Twitch omits it from the inventory entry.
				previous := out[index]
				if drop.Name == "" {
					drop.Name = previous.Name
				}
				if drop.GameName == "" {
					drop.GameName = previous.GameName
				}
				if drop.CampaignName == "" {
					drop.CampaignName = previous.CampaignName
				}
				if drop.CampaignStatus == "" {
					drop.CampaignStatus = previous.CampaignStatus
				}
				if drop.Required == 0 {
					drop.Required = previous.Required
				}
				if d.Self == nil {
					drop.IsClaimed = previous.IsClaimed
				}
				out[index] = drop
				continue
			}
			positions[key] = len(out)
			out = append(out, drop)
		}
	}
	for _, camp := range data.CurrentUser.DropCampaigns {
		appendCampaign(camp, false)
	}
	for _, camp := range data.CurrentUser.Inventory.Campaigns {
		appendCampaign(camp, true)
	}
	return out, nil
}

// Stream — минимальные данные об онлайн-стриме.
type Stream struct {
	Login       string
	Name        string
	Viewers     int
	ChannelID   string
	BroadcastID string
	GameID      string
	Game        string
}

// StreamMetadata loads the identifiers Twitch requires for watch-time events.
// Keep this separate from the directory query, whose smaller selection is
// known to return streams reliably for Android-issued tokens.
func (c *GQLClient) StreamMetadata(ctx context.Context, login string) (Stream, error) {
	const query = `query VideoPlayerStreamInfoOverlayChannel($channel: String!) {
  user(login: $channel) {
    id
    login
    broadcastSettings { game { id displayName } }
    stream { id game { id displayName } }
  }
}`
	var data struct {
		User *struct {
			ID                string `json:"id"`
			Login             string `json:"login"`
			BroadcastSettings struct {
				Game *struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
				} `json:"game"`
			} `json:"broadcastSettings"`
			Stream *struct {
				ID   string `json:"id"`
				Game *struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
				} `json:"game"`
			} `json:"stream"`
		} `json:"user"`
	}
	if err := c.do(ctx, "VideoPlayerStreamInfoOverlayChannel", query, map[string]any{"channel": login}, &data); err != nil {
		return Stream{}, err
	}
	if data.User == nil {
		return Stream{}, errors.New("Twitch не вернул данные пользователя канала")
	}
	if data.User.Stream == nil {
		return Stream{}, fmt.Errorf("%w: %s", ErrStreamOffline, login)
	}
	result := Stream{Login: data.User.Login, ChannelID: data.User.ID, BroadcastID: data.User.Stream.ID}
	game := data.User.Stream.Game
	if game == nil {
		game = data.User.BroadcastSettings.Game
	}
	if game != nil {
		result.GameID, result.Game = game.ID, game.DisplayName
	}
	return result, nil
}

// TopStreams — онлайн-стримы игры (предпочтительно с включёнными Drops).
func (c *GQLClient) TopStreams(ctx context.Context, gameName string, _ int) ([]Stream, error) {
	streams, _, err := c.categoryStreams(ctx, gameName, true, "")
	return streams, err
}

func (c *GQLClient) categoryStreams(ctx context.Context, gameName string, dropsOnly bool, cursor string) ([]Stream, string, error) {
	const gameSlugQuery = `query GameSlug($name: String!) { game(name: $name) { slug } }`
	const directoryHash = "cb5dc816e139dcb8a118f14b4b677d59abc224a4b016c4bc2bb00a47fe0ddec4"
	var slugData struct {
		Game *struct {
			Slug string `json:"slug"`
		} `json:"game"`
	}
	if err := c.do(ctx, "GameSlug", gameSlugQuery, map[string]any{"name": gameName}, &slugData); err != nil {
		return nil, "", fmt.Errorf("категория Twitch %q: %w", gameName, err)
	}
	if slugData.Game == nil || slugData.Game.Slug == "" {
		return nil, "", fmt.Errorf("категория Twitch %q не найдена", gameName)
	}
	var data struct {
		Game *struct {
			Streams struct {
				PageInfo struct{ HasNextPage bool }
				Edges    []struct {
					Cursor string
					Node   *struct {
						ID   string `json:"id"`
						Game *struct {
							ID          string `json:"id"`
							DisplayName string `json:"displayName"`
						} `json:"game"`
						Broadcaster *struct {
							ID          string `json:"id"`
							Login       string `json:"login"`
							DisplayName string `json:"displayName"`
						} `json:"broadcaster"`
						ViewersCount int `json:"viewersCount"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"streams"`
		} `json:"game"`
	}
	filters := []string{}
	if dropsOnly {
		filters = append(filters, "DROPS_ENABLED")
	}
	vars := map[string]any{
		"limit": 30, "slug": slugData.Game.Slug, "imageWidth": 50,
		"includeCostreaming": !dropsOnly, "sortTypeIsRecency": false,
		"options": map[string]any{
			"broadcasterLanguages": []string{}, "freeformTags": nil,
			"includeRestricted":      []string{"SUB_ONLY_LIVE"},
			"recommendationsContext": map[string]any{"platform": "web"},
			"sort":                   "VIEWER_COUNT", "systemFilters": filters,
			"tags": []string{}, "requestID": "JIRA-VXP-2397",
		},
	}
	if cursor != "" {
		vars["cursor"] = cursor
	}
	payload, err := json.Marshal(map[string]any{
		"operationName": "DirectoryPage_Game",
		"variables":     vars,
		"extensions":    map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": directoryHash}},
	})
	if err != nil {
		return nil, "", err
	}
	if err := c.doPayload(ctx, "DirectoryPage_Game", payload, &data); err != nil {
		return nil, "", fmt.Errorf("список Drops-стримов для %q: %w", gameName, err)
	}
	if data.Game == nil {
		return nil, "", fmt.Errorf("Twitch вернул пустую категорию для %q", gameName)
	}
	var out []Stream
	for _, e := range data.Game.Streams.Edges {
		if e.Node == nil || e.Node.Broadcaster == nil {
			continue
		}
		gameID, gameDisplay := "", gameName
		if e.Node.Game != nil {
			gameID, gameDisplay = e.Node.Game.ID, e.Node.Game.DisplayName
		}
		out = append(out, Stream{
			Login: e.Node.Broadcaster.Login, Name: e.Node.Broadcaster.Login,
			Viewers: e.Node.ViewersCount, ChannelID: e.Node.Broadcaster.ID,
			BroadcastID: e.Node.ID, GameID: gameID, Game: gameDisplay,
		})
	}
	next := ""
	if edges := data.Game.Streams.Edges; data.Game.Streams.PageInfo.HasNextPage && len(edges) > 0 {
		if last := edges[len(edges)-1].Cursor; last != cursor {
			next = last
		}
	}
	if len(out) == 0 {
		return nil, next, fmt.Errorf("%w: %q (slug %q, записей %d)", ErrNoDropsStreams, gameName, slugData.Game.Slug, len(data.Game.Streams.Edges))
	}
	return out, next, nil
}

// PlaybackAccessToken — токен и подпись для доступа к плейлистам канала.
func (c *GQLClient) PlaybackAccessToken(ctx context.Context, channelLogin string) (token, signature string, err error) {
	const query = `query PlaybackAccessToken($login: String!) {
  streamPlaybackAccessToken(channelName: $login, params: {
    platform: "web",
    playerBackend: "mediaplayer",
    playerType: "site"
  }) { value signature }
}`
	var data struct {
		StreamPlaybackAccessToken *struct {
			Value     string `json:"value"`
			Signature string `json:"signature"`
		} `json:"streamPlaybackAccessToken"`
	}
	err = c.do(ctx, "PlaybackAccessToken", query, map[string]any{"login": channelLogin}, &data)
	if err != nil {
		return "", "", err
	}
	if data.StreamPlaybackAccessToken == nil {
		return "", "", fmt.Errorf("playback token не получен для канала %s", channelLogin)
	}
	return data.StreamPlaybackAccessToken.Value, data.StreamPlaybackAccessToken.Signature, nil
}

// GetMasterPlaylist скачивает master-плейлист (несколько КБ текста).
func (c *GQLClient) GetMasterPlaylist(ctx context.Context, rawURL string) ([]byte, error) {
	body, err := c.getHLSPlaylist(ctx, rawURL, 256<<10)
	if err != nil {
		return nil, &playbackFailure{cause: err}
	}
	return body, nil
}

type HeartbeatResult struct {
	Method     string
	VariantURL string
	Warning    string
	HLSFailed  bool // Still unavailable after refreshing; select another channel.
}

// Preserve the cause for retry classification without exposing signed URLs.
type playbackFailure struct{ cause error }

func (e *playbackFailure) Error() string { return "HLS: " + safeHeartbeatError(e.cause) }
func (e *playbackFailure) Unwrap() error { return e.cause }

// WatchHeartbeat checks the media playlist and sends the minute-watched
// event. If the signed playlist has expired, it resolves a fresh one and
// retries once before reporting a partial success.
func (c *GQLClient) WatchHeartbeat(ctx context.Context, variantURL string, stream Stream) (HeartbeatResult, error) {
	result, playlistErr := c.WatchHLS(ctx, variantURL, stream)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	beaconErr := c.sendMinuteWatched(ctx, stream)
	switch {
	case playlistErr == nil && beaconErr == nil:
		result.Method = "HLS-сегменты и minute-watched beacon"
		return result, nil
	case playlistErr == nil:
		result.Warning = "minute-watched beacon: " + safeHeartbeatError(beaconErr)
		return result, nil
	case beaconErr == nil:
		result.Method = "minute-watched beacon"
		result.Warning = "HLS недоступен после обновления плейлиста: " + safeHeartbeatError(playlistErr)
		return result, nil
	default:
		return result, fmt.Errorf("оба сигнала не прошли (HLS: %s; beacon: %s)", safeHeartbeatError(playlistErr), safeHeartbeatError(beaconErr))
	}
}

// WatchHLS checks the playlist and segment headers, refreshing an expired URL once.
// It never sends a minute-watched event; the worker schedules that independently.
func (c *GQLClient) WatchHLS(ctx context.Context, variantURL string, stream Stream) (HeartbeatResult, error) {
	result := HeartbeatResult{VariantURL: variantURL}
	playlistErr := c.watchPlaylistSegments(ctx, variantURL)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if playlistErr != nil {
		freshVariant, refreshErr := c.refreshPlaylist(ctx, stream.Login)
		if refreshErr != nil {
			playlistErr = errors.Join(playlistErr, fmt.Errorf("playlist refresh failed: %w", refreshErr))
		} else {
			result.VariantURL = freshVariant
			playlistErr = c.watchPlaylistSegments(ctx, freshVariant)
			if playlistErr == nil {
				result.Warning = "HLS-плейлист обновлён после сбоя"
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if playlistErr != nil {
		result.HLSFailed = true
		return result, &playbackFailure{cause: playlistErr}
	}
	result.Method = "HLS-сегменты"
	return result, nil
}

func (c *GQLClient) SendMinuteWatched(ctx context.Context, stream Stream) error {
	if err := c.sendMinuteWatched(ctx, stream); err != nil {
		return fmt.Errorf("%s", safeHeartbeatError(err))
	}
	return nil
}

func (c *GQLClient) refreshPlaylist(ctx context.Context, channel string) (string, error) {
	token, signature, err := c.PlaybackAccessToken(ctx, channel)
	if err != nil {
		return "", err
	}
	master, err := c.GetMasterPlaylist(ctx, MasterPlaylistURL(channel, token, signature))
	if err != nil {
		return "", err
	}
	return PickLowestVariant(master)
}

var heartbeatHTTPStatusRE = regexp.MustCompile(`HTTP (\d{3})`)

// safeHeartbeatError keeps useful status details while preventing signed CDN
// URLs or request tokens from leaking into the per-account logs.
func safeHeartbeatError(err error) string {
	if err == nil {
		return "неизвестная ошибка"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "таймаут"
	}
	if errors.Is(err, context.Canceled) {
		return "запрос отменён"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "таймаут сети"
	}
	if match := heartbeatHTTPStatusRE.FindStringSubmatch(err.Error()); match != nil {
		return "HTTP " + match[1]
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "host rejected"):
		return "не разрешён HLS-хост"
	case strings.Contains(msg, "no segment urls"):
		return "плейлист без сегментов"
	case strings.Contains(msg, "missing required stream"):
		return "неполные данные стрима для beacon"
	default:
		// Keep the transport detail for diagnosis, but redact the entire URL:
		// Twitch playlist URLs contain signed tokens in both path and query.
		detail := heartbeatURLRE.ReplaceAllString(err.Error(), "<signed-url>")
		if len(detail) > 180 {
			detail = detail[:180] + "…"
		}
		return detail
	}
}

var heartbeatURLRE = regexp.MustCompile(`https?://[^\s"'<>]+`)

func (c *GQLClient) watchPlaylistSegments(ctx context.Context, playlistURL string) error {
	if !hlsHostAllowed(playlistURL) {
		return fmt.Errorf("HLS playlist host rejected")
	}
	body, err := c.getHLSPlaylist(ctx, playlistURL, 1<<20)
	if err != nil {
		return fmt.Errorf("media playlist: %w", err)
	}
	base, _ := url.Parse(playlistURL)
	segments := make([]string, 0, 8)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ref, err := url.Parse(line)
		if err != nil {
			continue
		}
		segments = append(segments, base.ResolveReference(ref).String())
	}
	if len(segments) == 0 {
		return fmt.Errorf("media playlist contains no segment URLs")
	}
	if len(segments) > 512 {
		return fmt.Errorf("media playlist has too many segments")
	}
	checked := make(map[string]struct{}, len(segments))
	for _, segmentURL := range segments {
		if !hlsHostAllowed(segmentURL) {
			return fmt.Errorf("HLS segment host rejected")
		}
		checked[segmentURL] = struct{}{}
		if _, ok := c.segments[segmentURL]; ok {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, segmentURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", androidUserAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HLS segment: HTTP %d", resp.StatusCode)
		}
	}
	c.segments = checked
	return nil
}

func (c *GQLClient) getHLSPlaylist(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if !hlsHostAllowed(rawURL) {
		return nil, fmt.Errorf("HLS playlist host rejected")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", androidUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HLS playlist HTTP %d", resp.StatusCode)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return nil, fmt.Errorf("HLS playlist returned JSON error")
	}
	return body, nil
}

func hlsHostAllowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range []string{"ttvnw.net", "live-video.net"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

var spadeURLRe = regexp.MustCompile(`"(?:beacon|spade)_?url":\s*"(https?://[^"\\]+)"`)
var settingsURLRe = regexp.MustCompile(`src="(https?://[^" ]+/config/settings\.[0-9a-f]{32}\.js)"`)

func (c *GQLClient) sendMinuteWatched(ctx context.Context, stream Stream) error {
	if stream.ChannelID == "" || stream.BroadcastID == "" || stream.GameID == "" || c.Account.UserID == "" {
		return fmt.Errorf("minute beacon: Twitch response is missing required stream/account identifiers")
	}
	beaconURL, err := c.spadeURL(ctx, stream.Login)
	if err != nil {
		return err
	}
	event := map[string]any{"event": "minute-watched", "properties": map[string]any{
		"broadcast_id": stream.BroadcastID, "channel_id": stream.ChannelID, "channel": stream.Login,
		"client_time": time.Now().UTC().Format(time.RFC3339), "game": stream.Game, "game_id": stream.GameID,
		"hidden": false, "is_live": true, "live": true, "logged_in": true,
		"minutes_logged": 1, "muted": false, "user_id": c.Account.UserID,
	}}
	events, err := json.Marshal([]any{event})
	if err != nil {
		return err
	}
	body := "data=" + url.QueryEscape(base64.StdEncoding.EncodeToString(events))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, beaconURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+c.Account.OAuth)
	req.Header.Set("Client-Id", c.cfg.ClientID)
	req.Header.Set("User-Agent", androidUserAgent)
	req.Header.Set("X-Device-Id", c.Account.DeviceID)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("minute beacon: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *GQLClient) spadeURL(ctx context.Context, channel string) (string, error) {
	c.spadeMu.Lock()
	if cached := c.spadeURLs[channel]; cached != "" {
		c.spadeMu.Unlock()
		return cached, nil
	}
	c.spadeMu.Unlock()
	pageURL := "https://www.twitch.tv/" + url.PathEscape(channel)
	page, err := c.getAuthenticatedText(ctx, pageURL)
	if err != nil {
		return "", err
	}
	match := spadeURLRe.FindSubmatch(page)
	if match == nil {
		if settings := settingsURLRe.FindSubmatch(page); settings != nil {
			settingsURL := string(settings[1])
			if !isTwitchHost(settingsURL) {
				return "", fmt.Errorf("settings URL host rejected for channel %s", channel)
			}
			page, err = c.getAuthenticatedText(ctx, settingsURL)
			if err != nil {
				return "", err
			}
			match = spadeURLRe.FindSubmatch(page)
		}
	}
	if match == nil {
		return "", fmt.Errorf("minute beacon URL not found for channel %s", channel)
	}
	beaconURL := string(match[1])
	if !isTwitchHost(beaconURL) {
		return "", fmt.Errorf("minute beacon host rejected for channel %s", channel)
	}
	c.spadeMu.Lock()
	c.spadeURLs[channel] = beaconURL
	c.spadeMu.Unlock()
	return beaconURL, nil
}

func isTwitchHost(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "twitch.tv" || strings.HasSuffix(host, ".twitch.tv")
}

func (c *GQLClient) getAuthenticatedText(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", c.cfg.ClientID)
	req.Header.Set("Authorization", "OAuth "+c.Account.OAuth)
	req.Header.Set("User-Agent", androidUserAgent)
	req.Header.Set("X-Device-Id", c.Account.DeviceID)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("channel metadata: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func (c *GQLClient) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", androidUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Не логируем URL целиком: в query лежат token/sig.
		return nil, fmt.Errorf("GET %s: HTTP %d", hostOnly(rawURL), resp.StatusCode)
	}
	return body, nil
}

func hostOnly(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// RedactLogLine is a final guard for signed URLs returned by transport errors.
func RedactLogLine(line string) string { return heartbeatURLRE.ReplaceAllString(line, "<url>") }
