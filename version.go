package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

func loadVersion() string {
	if content, err := os.ReadFile("version.txt"); err == nil {
		if version := strings.TrimSpace(string(content)); version != "" {
			return version
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func printStartupBanner(version, port string) {
	banner := ` _____       /\      __  __      /\     __          __     /\      _____
|  __ \     /  \    |  \/  |    /  \    \ \        / /    /  \    / ____|
| |__) |   / /\ \   | \  / |   / /\ \    \ \  /\  / /    / /\ \   \___ \
|  ___/   / ____ \  | |\/| |  / ____ \    \ \/  \/ /    / ____ \   ___) |
|_|      /_/    \_\ |_|  |_| /_/    \_\    \__/\__/    /_/    \_\ |____/`
	serviceArt := ` _____    ______   _____     ___    _____    _______   ______   _____
|  __ \  |  ____| |  __ \   / _ \  |  __ \  |__   __| |  ____| |  __ \
| |__) | | |__    | |__) | | | | | | |__) |    | |    | |__    | |__) |
|  _  /  |  __|   |  ___/  | |_| | |  _  /     | |    |  __|   |  _  /
|_| \_\  |______| |_|       \___/  |_| \_\     |_|    |______| |_| \_\`
	fmt.Printf("\033[1;36m%s\n%s\033[0m\nVersion: %s | Port: :%s\n\n", banner, serviceArt, version, port)
}
