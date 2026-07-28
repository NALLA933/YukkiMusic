/*
 * ● YukkiMusic
 * ○ A high-performance engine for streaming music in Telegram voicechats.
 */

package modules

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/core"
	state "main/internal/core/models"
	"main/internal/database"
	"main/internal/locales"
	"main/internal/platforms"
	"main/internal/utils"
)

const autoplayDataKey = "autoplay"

// recentTracksDataKey stores the IDs of the last few tracks that autoplay
// has played in a room, so the same song is never picked again too soon.
const recentTracksDataKey = "autoplay_recent"

// maxAutoplayHistory is how many recently played track IDs we remember.
// Once the list grows past this size, the oldest entries are dropped
// automatically (see rememberAutoplayTrack).
const maxAutoplayHistory = 10

func init() {
	helpTexts["/autoplay"] = `<i>Continue playback automatically when the queue ends.</i>

<u>Usage:</u>
<b>/autoplay</b> — Show the current state
<b>/autoplay on</b> — Enable autoplay
<b>/autoplay off</b> — Disable autoplay

When enabled, the bot searches YouTube for a related result after the last queued song. Manually queued songs always play first.`
}

func autoplayHandler(m *telegram.NewMessage) error {
	return handleAutoplay(m, false)
}

func cautoplayHandler(m *telegram.NewMessage) error {
	return handleAutoplay(m, true)
}

func handleAutoplay(m *telegram.NewMessage, cplay bool) error {
	r, err := getEffectiveRoom(m, cplay)
	if err != nil {
		m.Reply(err.Error())
		return telegram.ErrEndGroup
	}

	chatID := m.ChannelID()
	if !r.IsActiveChat() {
		m.Reply(F(chatID, "room_no_active"))
		return telegram.ErrEndGroup
	}

	arg := strings.ToLower(strings.TrimSpace(m.Args()))
	if arg == "" {
		state := F(chatID, "disabled")
		cmd := getCommand(m) + " on"
		if autoplayEnabled(r) {
			state = F(chatID, "enabled")
			cmd = getCommand(m) + " off"
		}
		m.Reply(F(chatID, "autoplay_current_state", locales.Arg{
			"state": state,
			"cmd":   cmd,
		}))
		return telegram.ErrEndGroup
	}

	var enabled bool
	switch arg {
	case "on", "enable", "true", "1":
		enabled = true
	case "off", "disable", "false", "0":
		enabled = false
	default:
		m.Reply(F(chatID, "invalid_bool"))
		return telegram.ErrEndGroup
	}

	if err := setAutoplay(r, enabled); err != nil {
		gologging.ErrorF("failed to save autoplay setting for %d: %v", r.ID, err)
		m.Reply(F(chatID, "generic_error", locales.Arg{"error": err.Error()}))
		return telegram.ErrEndGroup
	}
	state := F(chatID, "disabled")
	if enabled {
		state = F(chatID, "enabled")
	}
	m.Reply(F(chatID, "autoplay_updated", locales.Arg{
		"state": state,
		"user":  utils.MentionHTML(m.Sender),
	}))
	return telegram.ErrEndGroup
}

func autoplayEnabled(r interface{ GetData(string) (bool, any) }) bool {
	ok, value := r.GetData(autoplayDataKey)
	enabled, isBool := value.(bool)
	return ok && isBool && enabled
}

func setAutoplay(r *core.RoomState, enabled bool) error {
	if err := database.SetAutoplayEnabled(r.ID, enabled); err != nil {
		return err
	}
	r.SetData(autoplayDataKey, enabled)
	if !enabled {
		clearAutoplayPrefetch(r.ChatID)
	} else {
		scheduleAutoplayPrefetch(r)
	}
	return nil
}

// autoplayHistorySep separates the ID and title within a single stored
// history entry (see rememberAutoplayTrack). It is a control character that
// will never appear in a real track ID or title.
const autoplayHistorySep = "\x1f"

// autoplayHistoryEntry is one remembered "recently played" track.
type autoplayHistoryEntry struct {
	ID    string
	Title string
}

// recentAutoplayEntries decodes the stored history into ID+Title pairs.
func recentAutoplayEntries(r *core.RoomState) []autoplayHistoryEntry {
	ok, value := r.GetData(recentTracksDataKey)
	if !ok {
		return nil
	}
	raw, isSlice := value.([]string)
	if !isSlice {
		return nil
	}
	entries := make([]autoplayHistoryEntry, 0, len(raw))
	for _, item := range raw {
		parts := strings.SplitN(item, autoplayHistorySep, 2)
		entry := autoplayHistoryEntry{ID: parts[0]}
		if len(parts) > 1 {
			entry.Title = parts[1]
		}
		entries = append(entries, entry)
	}
	return entries
}

// recentAutoplayTracks returns the IDs of the tracks autoplay has played
// most recently in this room (oldest first, newest last). Autoplay uses
// this list to avoid selecting a song that already played recently.
func recentAutoplayTracks(r *core.RoomState) []string {
	entries := recentAutoplayEntries(r)
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// recentAutoplayTitles returns the titles of recently played tracks. This is
// used to catch different YouTube uploads of the same song (lyric video,
// slowed + reverb, remix, 8D audio, etc.) that have different track IDs but
// are really "the same song again".
func recentAutoplayTitles(r *core.RoomState) []string {
	entries := recentAutoplayEntries(r)
	titles := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Title != "" {
			titles = append(titles, e.Title)
		}
	}
	return titles
}

// rememberAutoplayTrack records that a track was just played by autoplay.
// It keeps only the most recent maxAutoplayHistory entries: once the list
// is full, the oldest track is automatically dropped (FIFO), so the
// history never grows unbounded and old songs become eligible again.
func rememberAutoplayTrack(r *core.RoomState, trackID, title string) {
	if trackID == "" {
		return
	}

	entries := recentAutoplayEntries(r)
	for _, e := range entries {
		if e.ID == trackID {
			return // already the most recent history, nothing to do
		}
	}

	entries = append(entries, autoplayHistoryEntry{ID: trackID, Title: title})
	if len(entries) > maxAutoplayHistory {
		entries = entries[len(entries)-maxAutoplayHistory:]
	}

	raw := make([]string, len(entries))
	for i, e := range entries {
		raw[i] = e.ID + autoplayHistorySep + e.Title
	}
	r.SetData(recentTracksDataKey, raw)
}

// hasAutoplayListener reports whether somebody other than the active assistant
// is still in the voice chat. A failed participant lookup intentionally keeps
// playback alive, so a transient Telegram API error cannot stop a live session.
func hasAutoplayListener(r *core.RoomState) bool {
	if r == nil || r.Assistant == nil || r.Assistant.Ntg == nil {
		return true
	}

	participants, err := r.Assistant.Ntg.GetParticipants(r.ID)
	if err != nil {
		gologging.WarnF("[autoplay] Could not get VC participants for %d: %v", r.ID, err)
		return true
	}

	assistantID := int64(0)
	if r.Assistant.Self != nil {
		assistantID = r.Assistant.Self.ID
	}
	for _, participant := range participants {
		if participant == nil || participant.Left || participant.Self {
			continue
		}
		if peer, ok := participant.Peer.(*telegram.PeerUser); ok && peer.UserID == assistantID {
			continue
		}
		return true
	}
	return false
}

// --- Autoplay prefetch -------------------------------------------------
//
// Resolves and downloads the *next* autoplay candidate in the background
// while the current track is still playing, so streamEndHandler / /skip can
// switch instantly instead of doing a fresh search + download only after
// the stream has already ended.

// prefetchedAutoplay is a fully resolved + already-downloaded autoplay
// candidate, cached against the track it was prepared for.
type prefetchedAutoplay struct {
	forTrackID string
	track      *state.Track
	filePath   string
}

var (
	prefetchMu      sync.Mutex
	prefetchCache   = map[int64]*prefetchedAutoplay{}
	prefetchCancels = map[int64]context.CancelFunc{}
)

// clearAutoplayPrefetch cancels any in-flight prefetch for chatID and drops
// whatever result had already been cached (and deletes its downloaded file,
// since nothing will ever play it now).
//
// Call this whenever the "obvious next song" stops being predictable from
// the currently playing track: /stop, /skip, autoplay turned off, or a
// track manually added to the queue.
func clearAutoplayPrefetch(chatID int64) {
	prefetchMu.Lock()
	cancel, hasCancel := prefetchCancels[chatID]
	cached, hasCached := prefetchCache[chatID]
	delete(prefetchCancels, chatID)
	delete(prefetchCache, chatID)
	prefetchMu.Unlock()

	if hasCancel && cancel != nil {
		cancel()
	}
	if hasCached && cached != nil && cached.filePath != "" {
		_ = os.Remove(cached.filePath)
	}
}

// takeAutoplayPrefetch returns (and removes) a cached, already-downloaded
// autoplay track if one was prepared for finishedTrackID. It also verifies
// the file still exists on disk before trusting it.
func takeAutoplayPrefetch(chatID int64, finishedTrackID string) (*state.Track, string) {
	prefetchMu.Lock()
	defer prefetchMu.Unlock()

	p, ok := prefetchCache[chatID]
	if !ok || p.forTrackID != finishedTrackID || p.track == nil || p.filePath == "" {
		return nil, ""
	}
	delete(prefetchCache, chatID)

	if _, err := os.Stat(p.filePath); err != nil {
		return nil, "" // file got cleaned up / moved from under us — caller falls back to a fresh download
	}
	return p.track, p.filePath
}

// scheduleAutoplayPrefetch starts a background goroutine that resolves and
// downloads the next autoplay candidate for the room's *currently playing*
// track. It is safe (and cheap) to call every time a track starts — it
// silently no-ops when autoplay is off, there's no current track, or manual
// tracks are already queued (autoplay wouldn't be used next anyway).
//
// Call this right after every successful r.Play(...) — in streamEndHandler,
// in /skip, and in your /play success path too — so a candidate is always
// ready ahead of time instead of only after the first autoplay transition.
func scheduleAutoplayPrefetch(r *core.RoomState) {
	if r == nil {
		return
	}
	chatID := r.ChatID
	current := r.Track()
	if current == nil || !autoplayEnabled(r) || len(r.Queue()) > 0 {
		return
	}

	clearAutoplayPrefetch(chatID)

	ctx, cancel := context.WithCancel(context.Background())
	prefetchMu.Lock()
	prefetchCancels[chatID] = cancel
	prefetchMu.Unlock()

	go func() {
		defer func() {
			prefetchMu.Lock()
			if prefetchCancels[chatID] != nil {
				delete(prefetchCancels, chatID)
			}
			prefetchMu.Unlock()
		}()

		if !hasAutoplayListener(r) {
			return
		}

		historyIDs := recentAutoplayTracks(r)
		historyTitles := recentAutoplayTitles(r)

		track, err := platforms.AutoplayTrack(current, historyIDs, historyTitles)
		if err != nil {
			gologging.DebugF("[autoplay-prefetch] no candidate for %d: %v", chatID, err)
			return
		}
		if ctx.Err() != nil {
			return
		}

		filePath, err := platforms.Download(ctx, track, nil)
		if err != nil {
			gologging.WarnF("[autoplay-prefetch] download failed for %d: %v", chatID, err)
			return
		}
		if ctx.Err() != nil {
			_ = os.Remove(filePath) // request was cancelled after download finished — clean up
			return
		}

		// The room may have moved on while we were working (skip, stop, a
		// manual track queued, autoplay turned off) — don't cache a stale
		// result, and don't leak the file we just downloaded for nothing.
		if r.Track() == nil || r.Track().ID != current.ID || len(r.Queue()) > 0 || !autoplayEnabled(r) {
			_ = os.Remove(filePath)
			return
		}

		prefetchMu.Lock()
		prefetchCache[chatID] = &prefetchedAutoplay{
			forTrackID: current.ID,
			track:      track,
			filePath:   filePath,
		}
		prefetchMu.Unlock()
	}()
}
