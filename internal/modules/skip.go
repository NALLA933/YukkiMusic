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

package modules

import (
	"context"
	"os"
	"strconv"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/core"
	state "main/internal/core/models"
	"main/internal/locales"
	"main/internal/platforms"
	"main/internal/utils"
)

func init() {
	helpTexts["/skip"] = `<i>Skip the currently playing track and play the next in queue.</i>

<u>Usage:</u>
<b>/skip</b> — Skip current track

<b>⚙️ Behavior:</b>
• Downloads next track in queue
• Starts playback automatically
• If queue is empty and loop is 0, stops playback

<b>🔒 Restrictions:</b>
• Only <b>chat admins</b> or <b>authorized users</b> can use this

<b>⚠️ Notes:</b>
• Cannot be undone
• If no tracks in queue, playback stops
• Loop count affects skip behavior`
}

func skipHandler(m *telegram.NewMessage) error {
	return handleSkip(m, false)
}

func cskipHandler(m *telegram.NewMessage) error {
	return handleSkip(m, true)
}

func handleSkip(m *telegram.NewMessage, cplay bool) error {
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

	mention := utils.MentionHTML(m.Sender)
	skipCount := 1
	// Holds the local file path when the track that ends up being played
	// next was a prefetched (already downloaded) autoplay candidate, so the
	// download step below can be skipped entirely.
	var prefetchedPath string

	if args := m.Args(); args != "" {
		parsed, parseErr := strconv.Atoi(args)
		if parseErr != nil {
			m.Reply(F(chatID, "skip_invalid_number"))
			return telegram.ErrEndGroup
		}

		queuedTracks := len(r.Queue())
		if queuedTracks == 0 {
			m.Reply(F(chatID, "skip_queue_empty_for_count"))
			return telegram.ErrEndGroup
		}

		if parsed < 1 || parsed > queuedTracks {
			m.Reply(F(chatID, "skip_count_exceeds_queue", locales.Arg{
				"requested": parsed,
				"available": queuedTracks,
			}))
			return telegram.ErrEndGroup
		}

		// /skip N means: skip current + N queued tracks.
		skipCount = parsed + 1
	}

	if len(r.Queue()) == 0 {
		ok, path := addAutoplayTrackForSkip(r)
		if !ok {
			scheduleOldPlayingMessage(r)
			core.DeleteRoom(r.ID)
			m.Reply(F(chatID, "skip_stopped", locales.Arg{
				"user": mention,
			}))
			return telegram.ErrEndGroup
		}
		prefetchedPath = path
	}

	r.SetLoop(0)

	for i := 1; i < skipCount; i++ {
		if len(r.Queue()) == 0 {
			ok, path := addAutoplayTrackForSkip(r)
			if !ok {
				scheduleOldPlayingMessage(r)
				core.DeleteRoom(r.ID)
				m.Reply(F(chatID, "skip_stopped", locales.Arg{
					"user": mention,
				}))
				return telegram.ErrEndGroup
			}
			prefetchedPath = path
		}
		_ = r.NextTrack()
		prefetchedPath = "" // that pick was just skipped over, not played — its path no longer applies
	}

	if len(r.Queue()) == 0 {
		ok, path := addAutoplayTrackForSkip(r)
		if !ok {
			scheduleOldPlayingMessage(r)
			core.DeleteRoom(r.ID)
			m.Reply(F(chatID, "skip_stopped", locales.Arg{
				"user": mention,
			}))
			return telegram.ErrEndGroup
		}
		prefetchedPath = path
	}

	t := r.NextTrack()
	if t == nil {

		scheduleOldPlayingMessage(r)
		core.DeleteRoom(r.ID)
		m.Reply(F(chatID, "skip_stopped", locales.Arg{
			"user": mention,
		}))
		return telegram.ErrEndGroup
	}

	statusMsg, err := core.Bot.SendMessage(
		chatID,
		F(chatID, "stream_downloading_next"),
	)
	if err != nil {
		gologging.ErrorF("[skip.go] err: %v", err)
	}

	path := ""
	if prefetchedPath != "" {
		if _, statErr := os.Stat(prefetchedPath); statErr == nil {
			path = prefetchedPath
		}
	}
	if path == "" {
		var downloadErr error
		path, downloadErr = platforms.Download(context.Background(), t, statusMsg)
		if downloadErr != nil {
			txt := F(chatID, "stream_download_fail", locales.Arg{
				"error": downloadErr.Error(),
			})

			if statusMsg != nil {
				utils.EOR(statusMsg, txt)
			} else {
				core.Bot.SendMessage(chatID, txt)
			}

			scheduleOldPlayingMessage(r)
			core.DeleteRoom(r.ID)
			return telegram.ErrEndGroup
		}
	}

	if err := r.Play(t, path, true); err != nil {
		txt := F(chatID, "stream_play_fail")
		if statusMsg != nil {
			utils.EOR(statusMsg, txt)
		} else {
			core.Bot.SendMessage(chatID, txt)
		}
		scheduleOldPlayingMessage(r)
		core.DeleteRoom(r.ID)
		return telegram.ErrEndGroup
	}

	rememberAutoplayTrack(r, t.ID, t.Title)
	scheduleAutoplayPrefetch(r)

	title := utils.ShortTitle(t.Title, 25)
	safeTitle := utils.EscapeHTML(title)

	msg := F(chatID, "stream_now_playing", locales.Arg{
		"url":      t.URL,
		"title":    safeTitle,
		"duration": utils.FormatDuration(t.Duration),
		"by":       t.Requester,
	})

	opt := &telegram.SendOptions{
		ParseMode:   "HTML",
		ReplyMarkup: core.GetPlayMarkup(chatID, r, false),
	}

	if t.Artwork != "" && shouldShowThumb(chatID) {
		opt.Media = utils.CleanURL(t.Artwork)
	}

	var newStatusMsg *telegram.NewMessage
	if statusMsg != nil {
		newStatusMsg, _ = utils.EOR(statusMsg, msg, opt)
	} else {
		newStatusMsg, _ = core.Bot.SendMessage(chatID, msg, opt)
	}

	if newStatusMsg != nil {
		r.SetStatusMsg(newStatusMsg)
	}

	return telegram.ErrEndGroup
}

// addAutoplayTrackForSkip makes /skip continue with a recommended track when
// the manual queue is empty and autoplay is enabled for this chat.
//
// It first checks for a prefetched candidate (one already resolved and
// downloaded in the background while the current track was playing — see
// autoplay_prefetch.go). If found, it's queued directly and its local file
// path is returned so the caller can skip a redundant download. If nothing
// was prefetched (e.g. autoplay was just turned on, or the prefetch hadn't
// finished yet), it falls back to a synchronous search exactly as before,
// and returns "" for the path so the caller downloads normally.
func addAutoplayTrackForSkip(r *core.RoomState) (bool, string) {
	if !autoplayEnabled(r) || !hasAutoplayListener(r) {
		return false, ""
	}

	current := r.Track()
	if current == nil {
		return false, ""
	}

	if cachedTrack, cachedPath := takeAutoplayPrefetch(r.ChatID, current.ID); cachedTrack != nil {
		r.AddTracksToQueue([]*state.Track{cachedTrack})
		return true, cachedPath
	}

	historyIDs := recentAutoplayTracks(r)
	historyTitles := recentAutoplayTitles(r)
	next, err := platforms.AutoplayTrack(current, historyIDs, historyTitles)
	if err != nil {
		gologging.WarnF("[skip] autoplay search failed for %s: %v", current.ID, err)
		return false, ""
	}
	r.AddTracksToQueue([]*state.Track{next})
	return true, ""
}
