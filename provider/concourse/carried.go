package concourse

import "github.com/lukaszraczylo/pipemaster/provider/emitkit"

// carry adds keys to the list of Ext keys whose behaviour the target file already holds.
func carry(ext map[string]any, keys ...string) {
	l, _ := ext[emitkit.CarriedKey].([]any)
	for _, k := range keys {
		l = append(l, k)
	}
	ext[emitkit.CarriedKey] = l
}
