package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	natsclient "github.com/nats-io/nats.go"
	"fyne.io/systray"
)

func (a *App) startTray(aiAPIURL, authURL, natsURL string, lucodePort int, nc *natsclient.Conn) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[tray] recovered from panic: %v", r)
			}
		}()
		systray.Run(
			func() { a.onTrayReady(aiAPIURL, authURL, natsURL, lucodePort, nc) },
			func() {},
		)
	}()
}

func (a *App) onTrayReady(aiAPIURL, authURL, natsURL string, lucodePort int, nc *natsclient.Conn) {
	systray.SetIcon(appIcon)
	systray.SetTitle("LuCode")
	systray.SetTooltip("LuCode")

	mLucode := systray.AddMenuItem("● LuCode — OK", "LuCode HTTP server")
	mLucode.Disable()
	mNATS := systray.AddMenuItem("● NATS", natsURL)
	mNATS.Disable()
	mAI := systray.AddMenuItem("● AI API", aiAPIURL)
	mAI.Disable()
	mAuth := systray.AddMenuItem("● Auth API", authURL)
	mAuth.Disable()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit LuCode", "Exit the application")

	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
			go a.CloseApp()
		}
	}()

	// Poll and update status every 10 s
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		update := func() {
			natsOK := nc != nil && nc.IsConnected()
			aiOK := checkHTTP(aiAPIURL + "/health")
			authOK := checkHTTP(authURL + "/health")

			mNATS.SetTitle(statusDot("NATS", natsOK))
			mAI.SetTitle(statusDot("AI API", aiOK))
			mAuth.SetTitle(statusDot("Auth API", authOK))
		}
		update()
		for range ticker.C {
			update()
		}
	}()
}

func statusDot(name string, ok bool) string {
	if ok {
		return fmt.Sprintf("● %s — OK", name)
	}
	return fmt.Sprintf("○ %s — offline", name)
}

func checkHTTP(url string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
