package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// resendSendCode sends one-time ITube confirmation or recovery codes.
// The API key is server-side only; never send it to the browser or commit it to Git.
func resendSendCode(to, code, purpose string) error {
	key := strings.TrimSpace(os.Getenv("ITUBE_RESEND_API_KEY"))
	sender := strings.TrimSpace(os.Getenv("ITUBE_RESEND_FROM"))
	if key == "" || sender == "" {
		return fmt.Errorf("Resend mail delivery is not configured")
	}
	if _, valid := accountEmail(to); !valid {
		return fmt.Errorf("recipient email is invalid")
	}
	if _, valid := accountEmail(sender); !valid {
		return fmt.Errorf("sender email is invalid")
	}

	subject := "ITube - email confirmation code"
	if purpose == "reset" {
		subject = "ITube - password reset code"
	} else if purpose != "verify" {
		return fmt.Errorf("unknown code purpose")
	}
	body := struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}{
		From:    sender,
		To:      []string{to},
		Subject: subject,
		Text:    fmt.Sprintf("ITube tasdiqlash kodi: %s\n\nKod 10 daqiqa amal qiladi. Uni hech kimga bermang.\nAgar siz kod soramagan bolsangiz, ushbu xatni etiborsiz qoldiring.", code),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("email API connection error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Return only HTTP status; do not log OTP or credentials.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Resend API returned HTTP %d", resp.StatusCode)
	}
	return nil
}
