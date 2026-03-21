package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WeatherData represents the response from the external weather API.
type WeatherData struct {
	FetchedAt   time.Time `json:"fetched_at" msgpack:"fetched_at"`
	City        string    `json:"city" msgpack:"city"`
	Description string    `json:"description" msgpack:"description"`
	TempC       float64   `json:"temp_c" msgpack:"temp_c"`
}

// Client wraps the external weather API
type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Fetch calls wttr.in (a free, no-key weather API) to get weather for a city.
// In production you'd use OpenWeatherMap or similar with a real apiKey.
func (c *Client) Fetch(ctx context.Context, city string) (*WeatherData, error) {
	url := fmt.Sprintf("https://wttr.in/%s?format=j1", city)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch weather for %q: %w", city, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// wttr.in JSON response (simplified parsing)
	var raw struct {
		CurrentCondition []struct {
			TempC       string `json:"temp_C"`
			WeatherDesc []struct {
				Value string `json:"value"`
			} `json:"weatherDesc"`
		} `json:"current_condition"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(raw.CurrentCondition) == 0 {
		return nil, fmt.Errorf("no weather data returned for %q", city)
	}

	cc := raw.CurrentCondition[0]
	desc := ""
	if len(cc.WeatherDesc) > 0 {
		desc = cc.WeatherDesc[0].Value
	}

	var tempC float64
	fmt.Sscanf(cc.TempC, "%f", &tempC)

	return &WeatherData{
		City:        city,
		TempC:       tempC,
		Description: desc,
		FetchedAt:   time.Now(),
	}, nil
}
