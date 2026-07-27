package database

// AutoplayEnabled reports whether autoplay should be restored for this chat.
func AutoplayEnabled(chatID int64) (bool, error) {
	settings, err := getChatSettings(chatID)
	if err != nil {
		return false, err
	}
	return settings.Autoplay, nil
}

// SetAutoplayEnabled persists the autoplay preference for this chat.
func SetAutoplayEnabled(chatID int64, enabled bool) error {
	return modifyChatSettings(chatID, func(s *ChatSettings) bool {
		if s.Autoplay == enabled {
			return false
		}
		s.Autoplay = enabled
		return true
	})
}
