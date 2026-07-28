/*
 * ● YukkiMusic
 * ○ A high-performance engine for streaming music in Telegram voicechats.
 *
 * Copyright (C) 2026 TheTeamVivek
 *
 * This program is free software: you can redistribute it and/or modify it under the
 * terms of the GNU General Public License as published by the Free Software Foundation,
 * either version 3 of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT ANY
 * WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
 * PARTICULAR PURPOSE. See the GNU General Public License for more details.
 *
 * Repository: https://github.com/TheTeamVivek/YukkiMusic
 */

package platforms

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/config"
	state "main/internal/core/models"
	"main/internal/utils"
)

type YouTubePlatform struct {
	name state.PlatformName
}

var (
	youtubeLinkRegex = regexp.MustCompile(
		`(?i)^(?:https?:\/\/)?(?:www\.|m\.|music\.)?(?:youtube\.com|youtu\.be)\/\S+`,
	)
	videoIDRe1 = regexp.MustCompile(
		`(?i)(?:youtube\.com/(?:watch\?v=|embed/|shorts/|live/)|youtu\.be/)([A-Za-z0-9_-]{11})`,
	)
	videoIDRe2    = regexp.MustCompile(`(?:v=|\/)([0-9A-Za-z_-]{11})`)
	playlistIDRe1 = regexp.MustCompile(
		`(?i)(?:youtube\.com|music\.youtube\.com).*(?:\?|&)list=([A-Za-z0-9_-]+)`,
	)
	playlistIDRe2 = regexp.MustCompile(`list=([0-9A-Za-z_-]+)`)
	youtubeCache  = utils.NewCache[string, []*state.Track](1 * time.Hour)
)

const (
	PlatformYouTube        state.PlatformName = "YouTube"
	innerTubeKey                              = "AIzaSyBOti4mM-6x9WDnZIjIeyEU21OpBXqWBgw"
	innerTubeClientVersion                    = "2.20250101.01.00"
	innerTubeClientName                       = "WEB"
)

var yt = &YouTubePlatform{
	name: PlatformYouTube,
}

func init() {
	Register(90, yt)
}

func (p *YouTubePlatform) Name() state.PlatformName {
	return p.name
}

func (p *YouTubePlatform) CanGetTracks(link string) bool {
	return youtubeLinkRegex.MatchString(link)
}

func (p *YouTubePlatform) GetTracks(
	input string,
	video bool,
) ([]*state.Track, error) {
	query := strings.TrimSpace(input)
	if query == "" {
		return nil, errors.New("empty query")
	}

	var (
		tracks []*state.Track
		err    error
	)

	if !youtubeLinkRegex.MatchString(query) {
		tracks, err = p.VideoSearch(query, false)
	} else {
		playlistID := p.extractPlaylistID(query)
		videoID := p.extractVideoID(query)

		switch {
		case playlistID != "" && videoID != "":
			tracks, err = p.handleCombined(query, videoID)

		case playlistID != "":
			tracks, err = p.handlePlaylist(query)

		default:
			tracks, err = p.handleTrackURL(query)
		}
	}

	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, errors.New("no tracks found")
	}

	return updateCached(tracks, video), nil
}

func (p *YouTubePlatform) handlePlaylist(
	rawURL string,
) ([]*state.Track, error) {
	cacheKey := "playlist:" + strings.ToLower(rawURL)
	if cached, ok := youtubeCache.Get(cacheKey); ok {
		return cached, nil
	}

	playlistID := p.extractPlaylistID(rawURL)
	if playlistID == "" {
		return nil, errors.New("invalid playlist url")
	}

	var (
		tracks []*state.Track
		err    error
	)

	if strings.HasPrefix(playlistID, "RD") {
		tracks, err = p.fetchMixPlaylist(playlistID, config.QueueLimit)
	} else {
		tracks, err = p.fetchPlaylist(playlistID, config.QueueLimit)
	}

	if err == nil && len(tracks) > 0 {
		youtubeCache.Set(cacheKey, tracks)
		return tracks, nil
	}

	return nil, fmt.Errorf("failed to fetch playlist: %w", err)
}

func (p *YouTubePlatform) handleCombined(rawURL, videoID string) ([]*state.Track, error) {
	vTracks, vErr := p.handleTrackURL(rawURL)
	pTracks, pErr := p.handlePlaylist(rawURL)

	if vErr == nil && pErr == nil && len(vTracks) > 0 {
		vid := vTracks[0].ID
		finalTracks := []*state.Track{vTracks[0]}
		for _, t := range pTracks {
			if t.ID != vid {
				finalTracks = append(finalTracks, t)
			}
		}
		return finalTracks, nil
	}

	if vErr == nil {
		return vTracks, nil
	}

	if pErr == nil {
		gologging.WarnF(
			"[YouTube] Failed to fetch video %s in combined URL: %v",
			videoID,
			vErr,
		)
		return pTracks, nil
	}

	return nil, fmt.Errorf("failed to fetch video (%v) and playlist (%v)", vErr, pErr)
}

func (p *YouTubePlatform) handleTrackURL(
	rawURL string,
) ([]*state.Track, error) {
	videoID := p.extractVideoID(rawURL)
	if videoID == "" {
		return nil, errors.New("invalid video url")
	}

	if cached, ok := youtubeCache.Get("track:" + videoID); ok &&
		len(cached) > 0 {
		return cached, nil
	}

	track, err := p.fetchVideo(videoID)
	if err == nil && track != nil {
		youtubeCache.Set("track:"+videoID, []*state.Track{track})
		return []*state.Track{track}, nil
	}

	for _, query := range []string{videoID, rawURL} {
		results, err := p.VideoSearch(query, true)
		if err != nil {
			continue
		}

		for _, t := range results {
			if t.ID == videoID {
				youtubeCache.Set("track:"+videoID, []*state.Track{t})
				return []*state.Track{t}, nil
			}
		}
	}

	return nil, errors.New("track not found")
}

func (p *YouTubePlatform) CanDownload(source state.PlatformName) bool {
	return false
}

func (p *YouTubePlatform) Download(
	_ context.Context,
	_ *state.Track,
	_ *telegram.NewMessage,
) (string, error) {
	return "", errors.New("youtube platform does not support downloading")
}

func (p *YouTubePlatform) VideoSearch(
	query string,
	singleOpt ...bool,
) ([]*state.Track, error) {
	single := false
	limit := config.QueueLimit
	if len(singleOpt) > 0 && singleOpt[0] {
		single = true
		limit = 1
	}

	cacheKey := "search:" + strings.TrimSpace(strings.ToLower(query))
	if arr, ok := youtubeCache.Get(cacheKey); ok {
		if single && len(arr) > 0 {
			return []*state.Track{arr[0]}, nil
		}
		if !single && len(arr) > 1 {
			return arr, nil
		}
	}

	tracks, err := p.performSearch(query, limit)
	if err != nil {
		return nil, fmt.Errorf("ytsearch failed: %w", err)
	}

	if len(tracks) == 0 {
		return nil, errors.New("no tracks found")
	}

	youtubeCache.Set(cacheKey, tracks)

	if single {
		return []*state.Track{tracks[0]}, nil
	}

	return tracks, nil
}

// AutoplayTrack returns a different YouTube result for a completed track.
// Search results are used as a reliable fallback for sources that do not expose
// a native related-videos feed.
//
// historyIDs/historyTitles hold the IDs and titles of tracks that were
// played recently (see modules.recentAutoplayTracks / recentAutoplayTitles).
// A candidate is rejected if its ID matches the history, or if its title is
// recognised as just another upload of a song that already played — e.g. a
// "(Lyrics)", "(Slowed + Reverb)", or "(Official Video)" version of the same
// song — since that would still feel like the same song playing again.
func AutoplayTrack(current *state.Track, historyIDs, historyTitles []string) (*state.Track, error) {
	if current == nil || strings.TrimSpace(current.Title) == "" {
		return nil, errors.New("current track has no title")
	}

	excludedIDs := autoplayExcludeSet(current.ID, historyIDs)
	excludedTitles := append([]string{current.Title}, historyTitles...)

	if current.Source == PlatformSpotify {
		if track, err := spotifyAutoplayTrack(current, excludedIDs, excludedTitles); err == nil {
			return track, nil
		}
	}

	if videoID := yt.extractVideoID(current.URL); videoID != "" {
		if track, err := yt.relatedTrack(videoID, current, excludedIDs, excludedTitles); err == nil {
			return track, nil
		}
	}

	results, err := yt.VideoSearch(current.Title, false)
	if err != nil {
		return nil, err
	}

	var candidates []*state.Track
	for _, track := range results {
		if track == nil || excludedIDs[track.ID] {
			continue
		}
		if track.Duration > 0 && track.Duration <= 60 {
			continue // skip YouTube Shorts
		}
		if isDuplicateSongTitle(track.Title, excludedTitles) {
			continue
		}
		candidates = append(candidates, track)
	}
	if len(candidates) == 0 {
		return nil, errors.New("no alternative autoplay result found")
	}

	track := candidates[rand.Intn(len(candidates))]
	track.Video = current.Video
	track.Requester = "Autoplay"
	return track, nil
}

// autoplayExcludeSet builds a lookup of track IDs that autoplay must not
// pick again: the track that was just played, plus everything still in the
// recent-history list.
func autoplayExcludeSet(currentID string, history []string) map[string]bool {
	set := make(map[string]bool, len(history)+1)
	if currentID != "" {
		set[currentID] = true
	}
	for _, id := range history {
		set[id] = true
	}
	return set
}

// --- Same-song / different-upload detection ---------------------------------
//
// YouTube frequently has many separate uploads of the exact same song: the
// official video, a lyric video, an "8D audio" edit, a "Slowed + Reverb"
// remix, a 1-hour loop, etc. Each has a different video ID, so ID-based
// deduplication alone lets autoplay bounce between these "different
// versions" of one song and feel broken. normalizeSongTitle strips that
// upload-specific noise so the underlying song name can be compared.

var (
	titleBracketNoiseRe = regexp.MustCompile(`\((?:[^()]*)\)|\[(?:[^\[\]]*)\]|\{(?:[^{}]*)\}`)
	titleFeatRe         = regexp.MustCompile(`(?i)\b(feat\.?|ft\.?|featuring)\b.*$`)
	titleNoiseWordsRe   = regexp.MustCompile(`(?i)\b(official( music)?( video| audio)?|music video|lyrics?( video)?|lyric video|visuali[sz]er|slowed( and | \+ )?reverb|slowed|reverb|nightcore|8d audio|8d|remix|remaster(ed)?|re[- ]?upload|cover|acoustic|unplugged|live( version| performance)?|hd|4k|1080p|720p|full( song| video| track)?|video song|audio( only)?|explicit|clean( version)?|radio edit|extended( mix)?|instrumental|karaoke|mp3|male version|female version|reprise|bass boosted|amv|edit|hq|version)\b`)
	titleNonAlnumRe     = regexp.MustCompile(`[^a-z0-9 ]+`)
	titleSpaceRe        = regexp.MustCompile(`\s+`)
)

// normalizeSongTitle reduces a track title to just the underlying song name,
// stripping upload-specific tags, bracketed noise, and punctuation.
func normalizeSongTitle(title string) string {
	t := strings.ToLower(title)
	t = titleBracketNoiseRe.ReplaceAllString(t, " ")
	t = titleFeatRe.ReplaceAllString(t, " ")
	t = titleNoiseWordsRe.ReplaceAllString(t, " ")
	t = titleNonAlnumRe.ReplaceAllString(t, " ")
	t = titleSpaceRe.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

// coreTitleSeparators mark where a title moves from "the song's own name"
// into artist/uploader/version-specific text — e.g. "Ishq - Faheem
// Abdullah", "Kesariya (Arijit Singh)", "Ishq ft. XYZ". Whatever comes
// before the earliest of these is treated as the song's core name.
var coreTitleSeparators = []string{" - ", " – ", " — ", " | ", "(", "[", " ft.", " ft ", " feat.", " feat "}

// extractCoreSongTitle returns the leading portion of a title before the
// first artist/version separator, normalized the same way as
// normalizeSongTitle. Different uploads of the same song (official video,
// a remix, a "slowed + reverb" edit, a cover by another channel) usually
// keep this leading segment identical even when everything after it is
// completely different, so it's a strong same-song signal on its own.
func extractCoreSongTitle(title string) string {
	lower := strings.ToLower(title)
	cut := len(lower)
	for _, sep := range coreTitleSeparators {
		if idx := strings.Index(lower, sep); idx != -1 && idx < cut {
			cut = idx
		}
	}
	return normalizeSongTitle(title[:cut])
}

// isSameSong reports whether two titles are almost certainly different
// uploads of the same underlying song, after stripping upload-noise.
func isSameSong(a, b string) bool {
	na, nb := normalizeSongTitle(a), normalizeSongTitle(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}

	shorter, longer := na, nb
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	// e.g. "tum hi ho" fully contained inside "tum hi ho unplugged version"
	if strings.Contains(longer, shorter) && float64(len(shorter))/float64(len(longer)) > 0.6 {
		return true
	}

	if titleSimilarity(na, nb) > 0.85 {
		return true
	}

	// Different uploaders often format everything AFTER the song name
	// completely differently — "Ishq - Faheem Abdullah, Rauhan Malik" vs
	// "Ishq (Slowed + Reverb) - Bass Boosted Nation" — which tanks
	// whole-title similarity even though it's the same song. Comparing just
	// the leading "core" segment (before the first separator/parenthesis/
	// feat.) catches this without needing the rest of the title to match.
	ca, cb := extractCoreSongTitle(a), extractCoreSongTitle(b)
	if len(ca) >= 4 && len(cb) >= 4 {
		if ca == cb {
			return true
		}
		if len(ca) >= 6 && len(cb) >= 6 && titleSimilarity(ca, cb) > 0.8 {
			return true
		}
	}

	return false
}

// isDuplicateSongTitle reports whether title matches any of excludedTitles
// closely enough to be considered the same underlying song.
func isDuplicateSongTitle(title string, excludedTitles []string) bool {
	for _, other := range excludedTitles {
		if isSameSong(title, other) {
			return true
		}
	}
	return false
}

// titleSimilarity returns a 0..1 similarity ratio based on edit distance.
func titleSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 1
	}
	return 1 - float64(levenshteinDistance(a, b))/float64(maxLen)
}

// levenshteinDistance computes the classic edit distance between two strings.
func levenshteinDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			curr[j] = min
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// relatedTrack asks YouTube's next endpoint for actual related videos. It is
// the same recommendation source that powers YouTube's own Up next results.
// It picks randomly among the eligible related videos (those not excluded
// by ID or recognised as the same underlying song) instead of always
// returning the first one.
func (p *YouTubePlatform) relatedTrack(videoID string, current *state.Track, excludedIDs map[string]bool, excludedTitles []string) (*state.Track, error) {
	var result map[string]any
	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innerTubeClientName,
				"clientVersion": innerTubeClientVersion,
			},
		},
		"videoId": videoID,
	}
	if err := p.callInnerTube("next", payload, &result); err != nil {
		return nil, err
	}

	var tracks []*state.Track
	p.parseNodes(result, &tracks, config.QueueLimit, "compactVideoRenderer")

	var candidates []*state.Track
	for _, track := range tracks {
		if track == nil || track.ID == videoID || excludedIDs[track.ID] {
			continue
		}
		if track.Duration > 0 && track.Duration <= 60 {
			continue // skip YouTube Shorts
		}
		if isDuplicateSongTitle(track.Title, excludedTitles) {
			continue
		}
		candidates = append(candidates, track)
	}
	if len(candidates) == 0 {
		return nil, errors.New("no related YouTube track found")
	}

	track := candidates[rand.Intn(len(candidates))]
	track.Video = current.Video
	track.Requester = "Autoplay"
	return track, nil
}

func (p *YouTubePlatform) extractPlaylistID(input string) string {
	m0 := playlistIDRe1.FindStringSubmatch(input)
	if len(m0) > 1 {
		return m0[1]
	}
	m := playlistIDRe2.FindStringSubmatch(input)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func (p *YouTubePlatform) extractVideoID(u string) string {
	m := videoIDRe1.FindStringSubmatch(u)
	if len(m) > 1 {
		return m[1]
	}
	m2 := videoIDRe2.FindStringSubmatch(u)
	if len(m2) > 1 {
		return m2[1]
	}
	return ""
}

func (p *YouTubePlatform) performSearch(query string, limit int) ([]*state.Track, error) {
	gologging.DebugF("[YouTube] Searching: %s", query)
	var result map[string]any

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innerTubeClientName,
				"clientVersion": innerTubeClientVersion,
				"hl":            "en-IN",
				"gl":            "IN",
			},
		},
		"query":  query,
		"params": "CAASAhAB",
	}

	err := p.callInnerTube("search", payload, &result)
	if err != nil {
		return nil, err
	}

	contents, ok := dig(
		result,
		"contents",
		"twoColumnSearchResultsRenderer",
		"primaryContents",
		"sectionListRenderer",
		"contents",
	).([]any)

	if !ok {
		return nil, errors.New("invalid search results")
	}

	var tracks []*state.Track
	p.parseNodes(contents, &tracks, limit, "videoRenderer")
	return tracks, nil
}

func (p *YouTubePlatform) fetchVideo(videoID string) (*state.Track, error) {
	gologging.DebugF("[YouTube] Fetching video: %s", videoID)
	var result map[string]any

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innerTubeClientName,
				"clientVersion": innerTubeClientVersion,
			},
		},
		"videoId": videoID,
	}

	err := p.callInnerTube("player", payload, &result)
	if err != nil {
		return nil, err
	}

	details, ok := dig(result, "videoDetails").(map[string]any)
	if !ok {
		return nil, errors.New("video details not found")
	}

	id := safeString(details["videoId"])
	title := safeString(details["title"])
	duration := atoi(safeString(details["lengthSeconds"]))
	thumb := getThumbnailURL(result)

	return &state.Track{
		URL:      "https://www.youtube.com/watch?v=" + id,
		Title:    title,
		ID:       id,
		Artwork:  thumb,
		Duration: duration,
		Source:   PlatformYouTube,
	}, nil
}

func (p *YouTubePlatform) fetchPlaylist(
	playlistID string,
	limit int,
) ([]*state.Track, error) {
	gologging.DebugF("[YouTube] Fetching playlist: %s", playlistID)
	var result map[string]any

	browseID := playlistID
	if !strings.HasPrefix(playlistID, "VL") {
		browseID = "VL" + playlistID
	}

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innerTubeClientName,
				"clientVersion": innerTubeClientVersion,
			},
		},
		"browseId": browseID,
	}

	err := p.callInnerTube("browse", payload, &result)
	if err != nil {
		return nil, err
	}

	var tracks []*state.Track
	p.parseNodes(result, &tracks, limit, "playlistVideoRenderer")
	return tracks, nil
}

func (p *YouTubePlatform) fetchMixPlaylist(
	playlistID string,
	limit int,
) ([]*state.Track, error) {
	gologging.DebugF("[YouTube] Fetching mix: %s", playlistID)
	var result map[string]any

	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innerTubeClientName,
				"clientVersion": innerTubeClientVersion,
			},
		},
		"playlistId": playlistID,
	}

	err := p.callInnerTube("next", payload, &result)
	if err != nil {
		return nil, err
	}

	items, ok := dig(
		result,
		"contents",
		"twoColumnWatchNextResults",
		"playlist",
		"playlist",
		"contents",
	).([]any)

	if !ok {
		return nil, errors.New("mix contents not found")
	}

	var tracks []*state.Track
	for _, item := range items {
		if limit > 0 && len(tracks) >= limit {
			break
		}

		if vid, ok := dig(item, "playlistPanelVideoRenderer").(map[string]any); ok {
			id := safeString(vid["videoId"])
			if id == "" {
				continue
			}

			title := safeString(dig(vid, "title", "simpleText"))
			thumb := getThumbnailURL(vid)
			duration := parseDuration(safeString(dig(vid, "lengthText", "simpleText")))

			t := &state.Track{
				URL:      "https://www.youtube.com/watch?v=" + id,
				Title:    title,
				ID:       id,
				Artwork:  thumb,
				Duration: duration,
				Source:   PlatformYouTube,
			}
			tracks = append(tracks, t)
			youtubeCache.Set("track:"+id, []*state.Track{t})
		}
	}

	return tracks, nil
}

func (p *YouTubePlatform) callInnerTube(endpoint string, body, result any) error {
	apiURL := fmt.Sprintf(
		"https://m.youtube.com/youtubei/v1/%s?key=%s",
		endpoint,
		innerTubeKey,
	)
	resp, err := rc.R().
		SetBody(body).
		SetResult(result).
		SetHeader("Content-Type", "application/json").
		SetHeader("User-Agent", "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Mobile Safari/537.36").
		Post(apiURL)
	if err != nil {
		return fmt.Errorf("innertube request failed: %w", err)
	}

	if resp.StatusCode() >= 400 {
		return fmt.Errorf("innertube error: %d", resp.StatusCode())
	}

	return nil
}

func (p *YouTubePlatform) parseNodes(
	node any,
	tracks *[]*state.Track,
	limit int,
	rendererKey string,
) {
	if limit > 0 && len(*tracks) >= limit {
		return
	}

	switch v := node.(type) {
	case []any:
		for _, item := range v {
			p.parseNodes(item, tracks, limit, rendererKey)
		}
	case map[string]any:
		if vid, ok := v[rendererKey].(map[string]any); ok {
			if rendererKey == "videoRenderer" && isLiveVideo(vid) {
				return
			}

			id := safeString(vid["videoId"])
			if id == "" {
				return
			}

			title := safeString(dig(vid, "title", "runs", 0, "text"))
			if title == "" {
				title = safeString(dig(vid, "title", "simpleText"))
			}
			thumb := getThumbnailURL(vid)
			durationText := safeString(dig(vid, "lengthText", "simpleText"))
			if durationText == "" {
				return
			}

			t := &state.Track{
				URL:      "https://www.youtube.com/watch?v=" + id,
				Title:    title,
				ID:       id,
				Artwork:  thumb,
				Duration: parseDuration(durationText),
				Source:   PlatformYouTube,
			}
			*tracks = append(*tracks, t)
			youtubeCache.Set("track:"+id, []*state.Track{t})
		} else {
			for _, val := range v {
				p.parseNodes(val, tracks, limit, rendererKey)
			}
		}
	}
}

func isLiveVideo(vid map[string]any) bool {
	if badges, ok := dig(vid, "badges").([]any); ok {
		for _, b := range badges {
			if style := safeString(dig(b, "metadataBadgeRenderer", "style")); style == "BADGE_STYLE_TYPE_LIVE_NOW" {
				return true
			}
		}
	}
	return strings.Contains(
		strings.ToLower(safeString(dig(vid, "viewCountText", "runs", 0, "text"))),
		"watching",
	)
}

func getThumbnailURL(vid map[string]any) string {
	thumbs, ok := dig(vid, "thumbnail", "thumbnails").([]any)
	if !ok || len(thumbs) == 0 {
		// Try player response structure
		thumbs, ok = dig(vid, "videoDetails", "thumbnail", "thumbnails").([]any)
	}

	if ok && len(thumbs) > 0 {
		if last, ok := thumbs[len(thumbs)-1].(map[string]any); ok {
			return safeString(last["url"])
		}
	}
	return ""
}

func updateCached(tracks []*state.Track, video bool) []*state.Track {
	out := make([]*state.Track, 0, len(tracks))
	for _, t := range tracks {
		if t == nil {
			continue
		}
		tc := *t
		tc.Video = video
		out = append(out, &tc)
	}
	return out
}

func dig(m any, path ...any) any {
	curr := m
	for _, p := range path {
		switch k := p.(type) {
		case string:
			if mm, ok := curr.(map[string]any); ok {
				curr = mm[k]
			} else {
				return nil
			}
		case int:
			if arr, ok := curr.([]any); ok && k < len(arr) {
				curr = arr[k]
			} else {
				return nil
			}
		}
	}
	return curr
}

func safeString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func parseDuration(s string) int {
	parts := strings.Split(s, ":")
	total := 0
	mult := 1
	for i := len(parts) - 1; i >= 0; i-- {
		n := atoi(parts[i])
		total += n * mult
		mult *= 60
	}
	return total
}

func atoi(s string) int {
	var n int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}
