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

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/core"
	state "main/internal/core/models"
	"main/internal/locales"
	"main/internal/platforms"
	"main/internal/utils"
	"main/ntgcalls"
)

func streamEndHandler(
	chatID int64,
	streamType ntgcalls.StreamType,
	_ ntgcalls.StreamDevice,
) {
	if streamType == ntgcalls.VideoStream {
		gologging.Debug("[onStreamEndHandler] Video stream ended, returning")
		return
	}

	gologging.DebugF("[onStreamEndHandler] Stream ended in chat %d", chatID)
	ass, err := core.Assistants.ForChat(chatID)
	if err != nil {
		gologging.ErrorF("Failed to get Assistant for %d: %v", chatID, err)
		return
	}
	r, ok := core.GetRoom(chatID, ass, false)
	if !ok {
		return
	}
	scheduleOldPlayingMessage(r)

	if ok, v := r.GetData("is_transitioning"); ok {
		if ok, v := v.(bool); ok && v {
			return
		}
	}

	r.SetData("is_transitioning", true)
	defer r.DeleteData("is_transitioning")

	cid := r.ChatID
	r.Parse()

	var t *state.Track
	var wasLooping bool
	var prefetchedFilePath string
	if len(r.Queue()) == 0 && r.Loop() == 0 {
		if current := r.Track(); autoplayEnabled(r) && current != nil {
			if !hasAutoplayListener(r) {
				r.SetData(autoplayDataKey, false)
				clearAutoplayPrefetch(chatID)
				core.DeleteRoom(chatID)
				core.Bot.SendMessage(cid, F(cid, "autoplay_stopped_no_listeners"))
				return
			}

			// A prefetched candidate was already resolved + downloaded while
			// `current` was still playing — use it directly and skip the
			// search + download that would otherwise happen only now.
			if cachedTrack, cachedPath := takeAutoplayPrefetch(chatID, current.ID); cachedTrack != nil {
				t = cachedTrack
				prefetchedFilePath = cachedPath
			} else {
				var autoplayErr error
				historyIDs := recentAutoplayTracks(r)
				historyTitles := recentAutoplayTitles(r)
				t, autoplayErr = platforms.AutoplayTrack(current, historyIDs, historyTitles)
				if autoplayErr != nil {
					gologging.ErrorF("[onStreamEndHandler] Autoplay search failed: %v", autoplayErr)
					core.DeleteRoom(chatID)
					core.Bot.SendMessage(cid, F(cid, "stream_queue_finished"))
					return
				}
			}
			r.AddTracksToQueue([]*state.Track{t})
			t = r.NextTrack()
		} else {
			core.DeleteRoom(chatID)
			core.Bot.SendMessage(cid, F(cid, "stream_queue_finished"))
			return
		}
	} else {
		clearAutoplayPrefetch(chatID) // a manually queued track is next, not an autoplay pick
		wasLooping = r.Loop() > 0
		t = r.NextTrack()
	}

	if t == nil {
		core.DeleteRoom(chatID)
		core.Bot.SendMessage(cid, F(cid, "stream_queue_finished"))
		return
	}

	statusText := F(cid, "stream_downloading_next")
	if wasLooping && t != nil && r.FilePath() != "" {
		statusText = F(cid, "cb_replaying")
	}

	statusMsg, err := core.Bot.SendMessage(
		cid,
		statusText,
	)
	if err != nil {
		gologging.ErrorF("[call.go] Failed to send msg: %v", err)
	}

	var filePath string
	if wasLooping && t != nil && r.FilePath() != "" {
		filePath = r.FilePath()
	} else if prefetchedFilePath != "" {
		if _, statErr := os.Stat(prefetchedFilePath); statErr == nil {
			filePath = prefetchedFilePath
		}
	}
	if filePath == "" {
		filePath, err = platforms.Download(context.Background(), t, statusMsg)
	}

	if err != nil {
		gologging.ErrorF(
			"[onStreamEndHandler] Download failed for %s: %v",
			t.URL,
			err,
		)
		utils.EOR(statusMsg, F(cid, "stream_download_fail", locales.Arg{
			"error": err.Error(),
		}))
		core.DeleteRoom(chatID)

		return
	}

	if err := r.Play(t, filePath, true); err != nil {
		gologging.ErrorF(
			"[onStreamEndHandler] Play failed for %s: %v",
			t.URL,
			err,
		)
		utils.EOR(statusMsg, F(cid, "stream_play_fail"))
		core.DeleteRoom(chatID)

		return
	}

	rememberAutoplayTrack(r, t.ID, t.Title)
	scheduleAutoplayPrefetch(r)

	title := utils.ShortTitle(t.Title, 25)
	safeTitle := utils.EscapeHTML(title)

	msgText := F(cid, "stream_now_playing", locales.Arg{
		"url":      t.URL,
		"title":    safeTitle,
		"duration": utils.FormatDuration(t.Duration),
		"by":       t.Requester,
	})

	opt := &telegram.SendOptions{
		ParseMode:   "HTML",
		ReplyMarkup: core.GetPlayMarkup(cid, r, false),
	}

	if t.Artwork != "" && shouldShowThumb(chatID) {
		opt.Media = utils.CleanURL(t.Artwork)
	}

	statusMsg, _ = utils.EOR(statusMsg, msgText, opt)
	r.SetStatusMsg(statusMsg)
}
