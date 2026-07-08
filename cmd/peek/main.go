// Command peek is a one-off Kalshi REST probe: for every candidate
// series prefix, reports how many markets exist in each status. Used to
// determine the correct series names for daily-low weather brackets.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/kalshi"
)

func main() {
	if _, err := env.LoadNearest(""); err != nil {
		fmt.Fprintln(os.Stderr, "warn: .env load:", err)
	}
	cfg, err := config.Load("")
	if err != nil {
		die(fmt.Errorf("config: %w", err))
	}
	pem, err := loadPEM(cfg.Kalshi)
	if err != nil {
		die(err)
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pem)
	if err != nil {
		die(fmt.Errorf("signer: %w", err))
	}
	client := kalshi.NewClient("https://api.elections.kalshi.com/trade-api/v2", signer)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Every weather-related series we saw in /series that looked HIGH/LOW/TEMP.
	candidates := []string{
		// Known-working daily HIGHs
		"KXHIGHNY", "KXHIGHMIA", "KXHIGHLAX", "KXHIGHCHI",
		// LOW candidates for the same 4 cities, all variants
		"KXLOWNY", "KXLOWNYC", "KXLOWTNY", "KXLOWTNYC",
		"KXLOWMIA", "KXLOWTMIA",
		"KXLOWLAX", "KXLOWTLAX", "KXLOWLA",
		"KXLOWCHI", "KXLOWTCHI",
		// Other weather series we spotted — for pattern-discovery
		"KXHIGHTMIN", "KXHIGHTATL", "KXLOWTSEA", "KXLOWTATL", "KXLOWTSFO", "KXLOWTDAL",
		"KXLOWAUS", "KXLOWDEN", "KXHIGHUS", "KXPHILHIGH", "KXHIGHNYD", "KXDVHIGH",
	}

	fmt.Printf("%-14s %6s  %s\n", "series", "total", "status histogram  —  sample tickers")
	fmt.Println("---")
	for _, s := range candidates {
		markets, _, err := client.GetMarketsPage(ctx, map[string]string{
			"series_ticker": s,
			"limit":         "200",
		})
		if err != nil {
			fmt.Printf("%-14s ERROR: %v\n", s, err)
			continue
		}
		byStatus := map[string]int{}
		samples := map[string][]string{}
		for _, m := range markets {
			byStatus[m.Status]++
			if len(samples[m.Status]) < 2 {
				samples[m.Status] = append(samples[m.Status], m.Ticker)
			}
		}
		keys := make([]string, 0, len(byStatus))
		for k := range byStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		hist := ""
		for _, k := range keys {
			hist += fmt.Sprintf("%dx%s ", byStatus[k], k)
		}
		fmt.Printf("%-14s %6d  %-30s", s, len(markets), hist)
		for _, k := range keys {
			for _, t := range samples[k] {
				fmt.Printf(" %s", t)
			}
		}
		fmt.Println()
	}
}

func loadPEM(k config.Kalshi) ([]byte, error) {
	if k.PrivateKey != "" {
		return []byte(k.PrivateKey), nil
	}
	if k.PrivateKeyPath != "" {
		return os.ReadFile(k.PrivateKeyPath)
	}
	return nil, errors.New("no PEM source configured")
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "peek:", err)
	os.Exit(1)
}
