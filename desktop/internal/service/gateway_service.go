package service

import (
	"agyp-desktop/internal/agyp"
	"fmt"
)

type GatewayService struct {
	client     *agyp.Client
	openWindow func()
	quitApp    func()
	setTheme   func(string)
}

func NewGatewayService(client *agyp.Client, openWindow func(), quitApp func(), setTheme func(string)) *GatewayService {
	return &GatewayService{
		client:     client,
		openWindow: openWindow,
		quitApp:    quitApp,
		setTheme:   setTheme,
	}
}

// OpenMainWindow focuses or opens the main dashboard window.
func (s *GatewayService) OpenMainWindow() (bool, error) {
	if s.openWindow != nil {
		s.openWindow()
		return true, nil
	}
	return false, nil
}

// QuitApp exits the desktop application.
func (s *GatewayService) QuitApp() (bool, error) {
	if s.quitApp != nil {
		s.quitApp()
		return true, nil
	}
	return false, nil
}

// SetTheme syncs native macOS window & popover appearance with the webview theme ("dark", "light", or "system").
func (s *GatewayService) SetTheme(mode string) (bool, error) {
	if s.setTheme != nil {
		s.setTheme(mode)
		return true, nil
	}
	return false, nil
}

// GetStats returns aggregated dashboard data for the requested day range.
func (s *GatewayService) GetStats(days int) (*agyp.DashboardData, error) {
	return s.client.GetDashboardData(days)
}

// GetLiveDiag returns live health, connection pool, and inflight metrics.
func (s *GatewayService) GetLiveDiag() (*agyp.LiveDiag, error) {
	return s.client.FetchDiag()
}

// GetRecentLogs returns recent rolling request logs with token speed.
func (s *GatewayService) GetRecentLogs(limit int) ([]agyp.RequestLogRow, error) {
	return s.client.GetRecentLogs(limit)
}

// GetSessionDetail returns on-demand context breakdown, session telemetry, and subagent rollup.
func (s *GatewayService) GetSessionDetail(sessionID string) (*agyp.SessionDetail, error) {
	return s.client.GetSessionDetail(sessionID)
}

// SwitchProfile switches the default profile at runtime without restarting sessions.
func (s *GatewayService) SwitchProfile(name string) (bool, error) {
	if err := s.client.SwitchProfile(name); err != nil {
		return false, fmt.Errorf("switch profile failed: %w", err)
	}
	return true, nil
}

// PauseProfile pauses or resumes a single profile at runtime.
func (s *GatewayService) PauseProfile(name string, paused bool) (bool, error) {
	if err := s.client.SetProfilePaused(name, paused); err != nil {
		return false, fmt.Errorf("pause profile failed: %w", err)
	}
	return true, nil
}

// OnlyProfile locks the gateway to a single profile and pauses all others.
func (s *GatewayService) OnlyProfile(name string) (bool, error) {
	if err := s.client.SetOnlyProfile(name); err != nil {
		return false, fmt.Errorf("only profile failed: %w", err)
	}
	return true, nil
}

// ResumeAllProfiles resumes all paused profiles back into the scheduling pool.
func (s *GatewayService) ResumeAllProfiles() (bool, error) {
	if err := s.client.ResumeAllProfiles(); err != nil {
		return false, fmt.Errorf("resume all profiles failed: %w", err)
	}
	return true, nil
}

// AddProfileOAuth starts browser OAuth login for a new profile.
func (s *GatewayService) AddProfileOAuth(name string) (*agyp.CommandResult, error) {
	return s.client.AddProfileOAuth(name)
}

// ImportKeychainProfile imports active macOS Keychain credentials as a profile.
func (s *GatewayService) ImportKeychainProfile(name string) (*agyp.CommandResult, error) {
	return s.client.ImportKeychainProfile(name)
}

// AddProfileToken adds a profile using a raw OAuth refresh token.
func (s *GatewayService) AddProfileToken(name, refreshToken, email string) (*agyp.CommandResult, error) {
	return s.client.AddProfileToken(name, refreshToken, email)
}

// RemoveProfile removes a profile from profiles.json.
func (s *GatewayService) RemoveProfile(name string, force bool) (*agyp.CommandResult, error) {
	return s.client.RemoveProfile(name, force)
}

// RenameProfile renames a profile in profiles.json.
func (s *GatewayService) RenameProfile(oldName, newName string) (*agyp.CommandResult, error) {
	return s.client.RenameProfile(oldName, newName)
}

// ExportCSV exports the aggregated data as CSV.
func (s *GatewayService) ExportCSV(days int) (string, error) {
	return s.client.ExportCSV(days)
}

// GetWasteScore returns token waste score and cache optimization recommendations.
func (s *GatewayService) GetWasteScore(days int) (*agyp.WasteScoreData, error) {
	return s.client.GetWasteScore(days)
}

// GetRoutingConfig returns smart routing rules and profile priority chain.
func (s *GatewayService) GetRoutingConfig() (*agyp.RoutingConfig, error) {
	return s.client.GetRoutingConfig()
}

// RunCheck runs intelligence probe (milk / pelican) on specified profile.
func (s *GatewayService) RunCheck(profile string, testType string) (*agyp.CheckResult, error) {
	return s.client.RunCheck(profile, testType)
}

// SendNotification triggers a macOS desktop notification.
func (s *GatewayService) SendNotification(title, message string) (bool, error) {
	if err := s.client.SendNotification(title, message); err != nil {
		return false, err
	}
	return true, nil
}

// GetRelayTimeline returns quota relay timeline events and rotation suggestions.
func (s *GatewayService) GetRelayTimeline() (*agyp.RelayTimelineData, error) {
	return s.client.GetRelayTimeline()
}

// GetConnectInfo returns client integration commands, LAN URL, and proxy configs.
func (s *GatewayService) GetConnectInfo() (*agyp.ConnectInfo, error) {
	return s.client.GetConnectInfo()
}

// TestEndpoint tests an endpoint path and profile directly through the gateway.
func (s *GatewayService) TestEndpoint(profile, path string) (*agyp.EndpointTestResult, error) {
	return s.client.TestEndpoint(profile, path)
}

// RestartGateway restarts the background gateway daemon.
func (s *GatewayService) RestartGateway(force bool) (*agyp.CommandResult, error) {
	return s.client.RestartGateway(force)
}

// StopGateway stops the gateway daemon.
func (s *GatewayService) StopGateway() (*agyp.CommandResult, error) {
	return s.client.StopGateway()
}

// StartGateway starts the gateway daemon.
func (s *GatewayService) StartGateway() (*agyp.CommandResult, error) {
	return s.client.StartGateway()
}

// GetDailyTopSessions returns daily top 5 sessions by token usage.
func (s *GatewayService) GetDailyTopSessions(days int) ([]agyp.DailySessionLeaderboard, error) {
	return s.client.GetDailyTopSessions(days)
}

// ResetWeeklyCycle forces the current weekly quota cycle start to today.
func (s *GatewayService) ResetWeeklyCycle() (bool, error) {
	if err := s.client.ResetWeeklyCycle(); err != nil {
		return false, err
	}
	return true, nil
}
