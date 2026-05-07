package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	gauth "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

const (
	tokenFileName  = "token.json"
	configDirName  = "gdrive-downloader"
	successHTML    = `<!doctype html><html><head><meta charset=utf-8><title>Done</title></head><body style="font-family:system-ui;text-align:center;margin-top:80px"><h2>Signed in.</h2><p>You can close this window and return to the app.</p></body></html>`
	failureHTMLFmt = `<!doctype html><html><head><meta charset=utf-8><title>Error</title></head><body style="font-family:system-ui;text-align:center;margin-top:80px"><h2>Sign-in failed</h2><p>%s</p></body></html>`
)

func userConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, configDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func tokenPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tokenFileName), nil
}

func loadToken() (*oauth2.Token, error) {
	p, err := tokenPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func saveToken(t *oauth2.Token) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func ForgetToken() error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func loadOAuthConfig(credentialsPath string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	cfg, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope, "https://www.googleapis.com/auth/userinfo.email")
	if err != nil {
		return nil, fmt.Errorf("parse credentials.json: %w", err)
	}
	return cfg, nil
}

// Authenticate runs the OAuth installed-app flow if no cached token is present
// or if the cached token is invalid. It returns an HTTP client + the token.
func Authenticate(ctx context.Context, credentialsPath string) (*http.Client, *oauth2.Token, error) {
	cfg, err := loadOAuthConfig(credentialsPath)
	if err != nil {
		return nil, nil, err
	}

	if t, err := loadToken(); err == nil && t != nil {
		ts := cfg.TokenSource(ctx, t)
		fresh, err := ts.Token()
		if err == nil && fresh.Valid() {
			if fresh.AccessToken != t.AccessToken {
				_ = saveToken(fresh)
			}
			return oauth2.NewClient(ctx, ts), fresh, nil
		}
	}

	t, err := interactiveAuth(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := saveToken(t); err != nil {
		return nil, nil, err
	}
	return cfg.Client(ctx, t), t, nil
}

func interactiveAuth(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for callback: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- result{err: errors.New("oauth state mismatch")}
			return
		}
		if errMsg := q.Get("error"); errMsg != "" {
			fmt.Fprintf(w, failureHTMLFmt, errMsg)
			resCh <- result{err: fmt.Errorf("oauth error: %s", errMsg)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resCh <- result{err: errors.New("missing code in callback")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(successHTML))
		resCh <- result{code: code}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	if err := openBrowser(authURL); err != nil {
		// Not fatal: user can copy the URL manually. Surface it.
		fmt.Fprintf(os.Stderr, "open this URL to authorize: %s\n", authURL)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := cfg.Exchange(ctx, res.code)
		if err != nil {
			return nil, fmt.Errorf("exchange code: %w", err)
		}
		return tok, nil
	}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// FetchUserEmail returns the email of the authenticated user.
func FetchUserEmail(ctx context.Context, client *http.Client) (string, error) {
	svc, err := gauth.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", err
	}
	info, err := svc.Userinfo.Get().Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return info.Email, nil
}
