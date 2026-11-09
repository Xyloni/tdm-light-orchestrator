package twitch

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MasterPlaylistURL строит URL master-плейлиста HLS для канала.
// Запрашивается только текстовый плейлист (несколько КБ), не видео.
func MasterPlaylistURL(login, token, sig string) string {
	q := url.Values{}
	q.Set("sig", sig)
	q.Set("token", token)
	q.Set("allow_source", "false") // не просим source-качество — экономим трафик
	q.Set("fast_bread", "true")
	q.Set("player_backend", "mediaplayer")
	q.Set("playlist_include_framerate", "true")
	q.Set("reassignments_supported", "true")
	q.Set("supported_codecs", "avc1")
	q.Set("cdm", "wv")
	q.Set("player_version", "1.24.0")
	return fmt.Sprintf("https://usher.ttvnw.net/api/channel/hls/%s.m3u8?%s",
		url.PathEscape(strings.ToLower(login)), q.Encode())
}

// PickLowestVariant разбирает master-плейлист и возвращает URL варианта
// с минимальным битрейтом (160p/audio-only) — heartbeat-ы почти ничего не весят.
func PickLowestVariant(master []byte) (string, error) {
	lines := strings.Split(string(master), "\n")
	best := ""
	bestBW := int(^uint(0) >> 1) // max int
	pendingBW := -1
	for _, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "#EXT-X-STREAM-INF") {
			pendingBW = parseBandwidth(ln)
			continue
		}
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		// Строка-URL варианта (идёт сразу после #EXT-X-STREAM-INF).
		bw := pendingBW
		if bw < 0 {
			bw = 0
		}
		if bw < bestBW {
			bestBW = bw
			best = ln
		}
		pendingBW = -1
	}
	if best == "" {
		return "", fmt.Errorf("в master-плейлисте не найдено вариантов потока")
	}
	return best, nil
}

func parseBandwidth(line string) int {
	i := strings.Index(line, "BANDWIDTH=")
	if i < 0 {
		return 0
	}
	j := i + len("BANDWIDTH=")
	k := j
	for k < len(line) && line[k] >= '0' && line[k] <= '9' {
		k++
	}
	n, err := strconv.Atoi(line[j:k])
	if err != nil {
		return 0
	}
	return n
}
