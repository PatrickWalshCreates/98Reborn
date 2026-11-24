// vmctl: drive a running lab VM over QMP (debug aid).
//
//	vmctl PORT shot FILE | keys k1+k2 [k3 ...] | key-file FILE (types the product key)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/win9x-modern/w9xm/internal/lab"
)

func main() {
	port, _ := strconv.Atoi(os.Args[1])
	vm, err := lab.Attach(port, "")
	if err != nil {
		fmt.Println("attach:", err)
		os.Exit(1)
	}
	switch os.Args[2] {
	case "hmp":
		var r json.RawMessage
		r, err = vm.QMP("human-monitor-command", map[string]any{"command-line": strings.Join(os.Args[3:], " ")})
		var out string
		json.Unmarshal(r, &out)
		fmt.Print(out)
	case "pause":
		_, err = vm.QMP("stop", nil)
	case "resume":
		_, err = vm.QMP("cont", nil)
	case "shot":
		err = vm.Screenshot(os.Args[3])
	case "keys":
		for _, chord := range os.Args[3:] {
			if err = vm.SendKeys(strings.Split(chord, "+")...); err != nil {
				break
			}
		}
	case "key-file":
		var k string
		if k, err = lab.ReadProductKey(os.Args[3]); err == nil {
			err = vm.TypeText(strings.ReplaceAll(k, "-", ""))
		}
	}
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
