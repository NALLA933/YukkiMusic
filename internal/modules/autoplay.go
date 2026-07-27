/*
 * ● YukkiMusic
 * ○ A high-performance engine for streaming music in Telegram voicechats.
 */

package modules

import (
	"strings"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/core"
	"main/internal/database"
	"main/internal/locales"
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
	return nil
}

// recentAutoplayTracks returns the IDs of the tracks autoplay has played
// most recently in this room (oldest first, newest last). Autoplay uses
// this list to avoid selecting a song that already played recently.
func recentAutoplayTracks(r *core.RoomState) []string {
	ok, value := r.GetData(recentTracksDataKey)
	if !ok {
		return nil
	}
	history, isSlice := value.([]string)
	if !isSlice {
		return nil
	}
	return history
}

// rememberAutoplayTrack records that a track was just played by autoplay.
// It keeps only the most recent maxAutoplayHistory entries: once the list
// is full, the oldest track ID is automatically dropped (FIFO), so the
// history never grows unbounded and old songs become eligible again.
func rememberAutoplayTrack(r *core.RoomState, trackID string) {
	if trackID == "" {
		return
	}

	history := recentAutoplayTracks(r)
	for _, id := range history {
		if id == trackID {
			return // already the most recent history, nothing to do
		}
	}

	history = append(history, trackID)
	if len(history) > maxAutoplayHistory {
		history = history[len(history)-maxAutoplayHistory:]
	}
	r.SetData(recentTracksDataKey, history)
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
