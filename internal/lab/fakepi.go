package lab

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteFakePi(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "fake-pi")
	script := `#!/bin/sh
IFS= read -r _prompt || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[],"api":"anthropic-messages","provider":"fake","model":"fake-model","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"total":0}}}}'
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"fake lab response"},"message":{"role":"assistant","content":[{"type":"text","text":"fake lab response"}],"api":"anthropic-messages","provider":"fake","model":"fake-model","usage":{"input":1,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":4,"cost":{"total":0}}}}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"fake lab response"}],"usage":{"input":1,"output":3,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}}}]}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", fmt.Errorf("write fake pi: %w", err)
	}
	return path, nil
}
