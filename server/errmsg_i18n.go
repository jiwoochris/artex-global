package server

import "github.com/Autumn-27/artex/config"

// errCatalog holds the Spanish translation of every user-facing backend message.
// English is the source language of this repository (see errmsg_catalog.go): the
// writeErr response bodies and some writeJSON error fields are English constants
// or inline English literals, and the catalog maps that exact English text to its
// Spanish translation for the es output language.
//
// The key is extracted byte for byte from the English source constant/literal, so
// the runtime value passed into writeErr and the key can never drift apart.
//
// trMsg is total: an unknown language, an unwired hook or a message that has no
// catalog entry returns the English input unchanged. Nothing user-visible is ever
// lost — a missing translation degrades to the source language.
//
// Format-string constants (%d, %s, %w …) must be translated as a template and
// formatted afterwards — fmt.Errorf(trMsg(errXxxFmt), a, b) — because formatting
// first changes the message and the lookup would miss. The same rule applies to
// fragments concatenated with dynamic text: translate only the prefix.

// trMsg returns the active output language's rendering of an English user-facing
// message. The active language comes from config.ActiveLanguage, which reflects
// the runtime setting (settings table), so a switch applies to the next response.
func trMsg(english string) string {
	if m, ok := errCatalog[english]; ok {
		if v, ok2 := m[config.ActiveLanguage()]; ok2 && v != "" {
			return v
		}
	}
	return english
}

// logT translates an operator-facing console log template (config.T) so server
// log.Printf sites can wrap a template without each file importing config.
func logT(english string) string { return config.T(english) }

// loadOutputLanguage resolves the user-facing output language from the source of
// truth: the runtime setting (settings table) over config.Language() (ARTEX_LANG >
// config file > en). Call it only to (re)fill the cache; it does a DB query and a
// config file read.
func (s *Server) loadOutputLanguage() string {
	if s.m != nil && s.m.pg != nil {
		if v, ok, _ := s.m.pg.GetSetting(settingOutputLanguage); ok {
			if nv := config.NormalizeLanguage(v); nv != "" {
				return nv
			}
		}
	}
	return config.Language()
}

// outputLanguage returns the cached output language, resolving it on first use.
// An empty cache entry (never stored before the wiring ran) falls back to
// resolving, and an unset/invalid source resolves to English.
func (s *Server) outputLanguage() string {
	if v, ok := s.outputLang.Load().(string); ok && v != "" {
		return v
	}
	v := s.loadOutputLanguage()
	if v == "" {
		v = config.DefaultLanguage
	}
	s.outputLang.Store(v)
	return v
}
