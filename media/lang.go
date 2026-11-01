package media

import "strings"

// Files tag languages with ISO 639-2 codes, which come in two flavours for
// some languages: bibliographic ("ger", "fre", "dut") and terminologic
// ("deu", "fra", "nld"). People also write ISO 639-1 ("de"). Language maps
// all of them onto one code, so "de", "ger" and "deu" are the same language.

// terminologic maps bibliographic ISO 639-2/B codes onto their 639-2/T form.
var terminologic = map[string]string{
	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho",
	"cze": "ces", "dut": "nld", "fre": "fra", "geo": "kat", "ger": "deu",
	"gre": "ell", "ice": "isl", "mac": "mkd", "mao": "mri", "may": "msa",
	"per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym",
}

// twoLetter maps ISO 639-1 codes onto ISO 639-2/T.
var twoLetter = map[string]string{
	"af": "afr", "ar": "ara", "bg": "bul", "bn": "ben", "bs": "bos",
	"ca": "cat", "cs": "ces", "cy": "cym", "da": "dan", "de": "deu",
	"el": "ell", "en": "eng", "es": "spa", "et": "est", "eu": "eus",
	"fa": "fas", "fi": "fin", "fil": "fil", "fr": "fra", "ga": "gle",
	"gl": "glg", "he": "heb", "hi": "hin", "hr": "hrv", "hu": "hun",
	"hy": "hye", "id": "ind", "is": "isl", "it": "ita", "ja": "jpn",
	"ka": "kat", "kk": "kaz", "ko": "kor", "lt": "lit", "lv": "lav",
	"mk": "mkd", "ms": "msa", "mt": "mlt", "nb": "nob", "nl": "nld",
	"nn": "nno", "no": "nor", "pl": "pol", "pt": "por", "ro": "ron",
	"ru": "rus", "sk": "slk", "sl": "slv", "sq": "sqi", "sr": "srp",
	"sv": "swe", "ta": "tam", "te": "tel", "th": "tha", "tl": "tgl",
	"tr": "tur", "uk": "ukr", "ur": "urd", "vi": "vie", "zh": "zho",
}

// Language returns the canonical code for a language tag or config entry,
// or "" for an empty or undetermined ("und") tag.
func Language(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" || c == "und" {
		return ""
	}
	if t, ok := twoLetter[c]; ok {
		return t
	}
	if t, ok := terminologic[c]; ok {
		return t
	}
	return c
}
