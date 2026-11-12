package bus

import "testing"

func TestPortableTextValidation(t *testing.T) {
	for _, text := range []string{"nul\x00text", string([]byte{0xff})} {
		requireCode(t, validateText(text, "body", 65536, false), CodeInvalidArgument)
	}
	for _, text := range []string{"a newline\nand emoji 🚌", `literal \u0000`, "日本語"} {
		requireNoError(t, validateText(text, "body", 65536, false))
	}
}
