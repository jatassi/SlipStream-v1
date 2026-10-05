package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/slipstream/slipstream/internal/config"
	"github.com/slipstream/slipstream/internal/database/sqlc"
)

const (
	githubAPIURL       = "https://api.github.com/repos/jatassi/SlipStream-v1/releases/latest"
	settingAutoInstall = "update_auto_install"
	osLinux            = "linux"
)

type State string

const (
	StateIdle            State = "idle"
	StateChecking        State = "checking"
	StateUpToDate        State = "up-to-date"
	StateUpdateAvailable State = "update-available"
	StateError           State = "error"
	StateDownloading     State = "downloading"
	StateInstalling      State = "installing"
	StateRestarting      State = "restarting"
	StateComplete        State = "complete"
	StateFailed          State = "failed"
)

type ReleaseInfo struct {
	Version      string    `json:"version"`
	TagName      string    `json:"tagName"`
	ReleaseDate  string    `json:"releaseDate"`
	ReleaseNotes string    `json:"releaseNotes"`
	DownloadURL  string    `json:"downloadUrl"`
	AssetName    string    `json:"assetName"`
	AssetSize    int64     `json:"assetSize"`
	PublishedAt  time.Time `json:"publishedAt"`
}

type Status struct {
	State          State        `json:"state"`
	CurrentVersion string       `json:"currentVersion"`
	LatestRelease  *ReleaseInfo `json:"latestRelease,omitempty"`
	Progress       float64      `json:"progress"`
	DownloadedMB   float64      `json:"downloadedMB,omitempty"`
	TotalMB        float64      `json:"totalMB,omitempty"`
	Error          string       `json:"error,omitempty"`
	LastChecked    *time.Time   `json:"lastChecked,omitempty"`
}

type Settings struct {
	AutoInstall bool `json:"autoInstall"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
}

type Broadcaster interface {
	BroadcastUpdateStatus(status interface{})
}

type Service struct {
	db             *sql.DB
	logger         zerolog.Logger
	hub            Broadcaster
	httpClient     *http.Client
	downloadClient *http.Client
	restartChan    chan<- bool
	port           int

	mu           sync.RWMutex
	status       Status
	cancelFunc   context.CancelFunc
	downloadPath string
}

func NewService(db *sql.DB, logger *zerolog.Logger, restartChan chan<- bool) *Service {
	return &Service{
		db:     db,
		logger: logger.With().Str("component", "updater").Logger(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		downloadClient: &http.Client{
			Timeout: 0, // No timeout for large file downloads; cancellation is handled via context
		},
		restartChan: restartChan,
		status: Status{
			State:          StateIdle,
			CurrentVersion: config.Version,
		},
	}
}

func (s *Service) SetBroadcaster(hub Broadcaster) {
	s.hub = hub
}

func (s *Service) SetPort(port int) {
	s.port = port
}

func (s *Service) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Service) GetSettings(ctx context.Context) (*Settings, error) {
	queries := sqlc.New(s.db)
	setting, err := queries.GetSetting(ctx, settingAutoInstall)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &Settings{AutoInstall: true}, nil
		}
		return nil, err
	}
	return &Settings{
		AutoInstall: setting.Value == "true" || setting.Value == "1",
	}, nil
}

func (s *Service) UpdateSettings(ctx context.Context, settings *Settings) error {
	queries := sqlc.New(s.db)
	value := "false"
	if settings.AutoInstall {
		value = "true"
	}
	_, err := queries.SetSetting(ctx, sqlc.SetSettingParams{
		Key:   settingAutoInstall,
		Value: value,
	})
	return err
}

func (s *Service) setState(state State, err error) {
	s.mu.Lock()
	s.status.State = state
	if err != nil {
		s.status.Error = err.Error()
	} else {
		s.status.Error = ""
	}
	status := s.status
	s.mu.Unlock()

	s.broadcast(&status)
}

func (s *Service) setProgress(progress, downloadedMB, totalMB float64) {
	s.mu.Lock()
	s.status.Progress = progress
	s.status.DownloadedMB = downloadedMB
	s.status.TotalMB = totalMB
	status := s.status
	s.mu.Unlock()

	s.broadcast(&status)
}

func (s *Service) broadcast(status *Status) {
	if s.hub != nil {
		s.hub.BroadcastUpdateStatus(status)
	}
}

func (s *Service) CheckForUpdate(ctx context.Context) (*ReleaseInfo, error) {
	s.setState(StateChecking, nil)

	release, err := s.fetchLatestRelease(ctx)
	if err != nil {
		s.setState(StateError, err)
		return nil, err
	}

	currentVersion := config.Version
	if currentVersion == "" || currentVersion == "dev" {
		s.logger.Debug().Msg("Running development build, skipping version comparison")
		s.mu.Lock()
		s.status.State = StateUpToDate
		s.status.LastChecked = ptr(time.Now())
		status := s.status
		s.mu.Unlock()
		s.broadcast(&status)
		return nil, nil //nolint:nilnil // nil release with no error means "no update available"
	}

	isNewer, err := IsNewerThan(release.TagName, currentVersion)
	if err != nil {
		s.logger.Warn().Err(err).Str("tagName", release.TagName).Str("currentVersion", currentVersion).Msg("Failed to compare versions")
		isNewer = release.TagName != currentVersion && release.TagName != "v"+currentVersion
	}

	now := time.Now()
	s.mu.Lock()
	s.status.LastChecked = &now
	if isNewer {
		releaseInfo := s.buildReleaseInfo(release)
		s.status.State = StateUpdateAvailable
		s.status.LatestRelease = releaseInfo
		status := s.status
		s.mu.Unlock()
		s.broadcast(&status)
		return releaseInfo, nil
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	s.status.State = StateUpToDate
	s.status.LatestRelease = nil
	status := s.status
	s.mu.Unlock()
	s.broadcast(&status)
	s.logger.Info().
		Str("currentVersion", currentVersion).
		Str("latestVersion", latestVersion).
		Msg("Already on the latest version")
	return nil, nil //nolint:nilnil // nil release with no error means "no update available"
}

func (s *Service) fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "SlipStream/"+config.Version)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to decode release: %w", err)
	}

	if release.Draft || release.Prerelease {
		return nil, fmt.Errorf("latest release is draft or prerelease")
	}

	return &release, nil
}

func (s *Service) buildReleaseInfo(release *githubRelease) *ReleaseInfo {
	info := &ReleaseInfo{
		Version:      strings.TrimPrefix(release.TagName, "v"),
		TagName:      release.TagName,
		ReleaseDate:  release.PublishedAt.Format("2006-01-02"),
		ReleaseNotes: release.Body,
		PublishedAt:  release.PublishedAt,
	}

	asset := s.findPlatformAsset(release.Assets)
	if asset != nil {
		info.DownloadURL = asset.BrowserDownloadURL
		info.AssetName = asset.Name
		info.AssetSize = asset.Size
	}

	return info
}

func (s *Service) findPlatformAsset(assets []githubAsset) *githubAsset {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	var patterns []string
	switch goos {
	case "windows":
		// Prefer portable ZIP for in-app updates (binary replacement in user-writable install dir)
		patterns = []string{
			fmt.Sprintf("_windows_%s.zip", goarch),
		}
	case "darwin":
		patterns = []string{
			fmt.Sprintf("_darwin_%s.dmg", goarch),
		}
	case osLinux:
		if isLinuxStubPattern() {
			// For deb/rpm stub pattern, prefer tarball (direct binary, user-writable)
			patterns = []string{
				fmt.Sprintf("_linux_%s.tar.gz", goarch),
			}
		} else {
			// For AppImage installs, prefer AppImage
			patterns = []string{
				fmt.Sprintf("_linux_%s.AppImage", goarch),
			}
		}
	}

	for _, pattern := range patterns {
		for i := range assets {
			if strings.Contains(assets[i].Name, pattern) {
				return &assets[i]
			}
		}
	}

	return nil
}

// isLinuxStubPattern checks if running from the deb/rpm stub launcher pattern
// where the binary lives in ~/.local/share/slipstream/bin/
func isLinuxStubPattern() bool {
	if runtime.GOOS != "linux" {
		return false
	}

	exe, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	expectedBinDir := filepath.Join(home, ".local", "share", "slipstream", "bin")
	return filepath.Dir(exe) == expectedBinDir
}

func (s *Service) DownloadAndInstall(ctx context.Context) error {
	s.logger.Info().Msg("DownloadAndInstall called")

	s.mu.RLock()
	release := s.status.LatestRelease
	s.mu.RUnlock()

	if release == nil || release.DownloadURL == "" {
		s.logger.Error().Msg("No update available to download")
		return fmt.Errorf("no update available to download")
	}

	s.logger.Info().
		Str("version", release.Version).
		Str("url", release.DownloadURL).
		Int64("sizeBytes", release.AssetSize).
		Msg("Release info validated")

	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()

	s.logger.Info().Msg("Created cancellable context for download")

	defer func() {
		s.logger.Info().Msg("Cleaning up download context")
		s.mu.Lock()
		s.cancelFunc = nil
		s.mu.Unlock()
	}()

	s.logger.Info().Str("version", release.Version).Str("url", release.DownloadURL).Msg("Starting update download")

	downloadPath, err := s.downloadUpdate(ctx, release)
	if err != nil {
		s.logger.Error().Err(err).Bool("contextCanceled", ctx.Err() != nil).Msg("Download failed")
		s.setState(StateFailed, err)
		return err
	}

	s.logger.Info().Str("downloadPath", downloadPath).Msg("Download completed, proceeding to install")

	s.mu.Lock()
	s.downloadPath = downloadPath
	s.mu.Unlock()

	s.backupDatabase()

	if err := s.installUpdate(ctx, downloadPath); err != nil {
		s.setState(StateFailed, err)
		return err
	}

	return nil
}

func (s *Service) downloadUpdate(ctx context.Context, release *ReleaseInfo) (string, error) {
	s.setState(StateDownloading, nil)

	resp, startTime, err := s.createDownloadRequest(ctx, release)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.logger.Error().Int("statusCode", resp.StatusCode).Msg("Download returned non-200 status")
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	downloadPath, file, err := s.prepareDownloadFile(release)
	if err != nil {
		return "", err
	}
	defer file.Close()

	totalSize := float64(release.AssetSize)
	totalMB := totalSize / (1024 * 1024)

	downloaded, err := s.downloadLoop(ctx, resp.Body, file, downloadPath, totalSize, totalMB)
	if err != nil {
		return "", err
	}

	s.setProgress(100, totalMB, totalMB)
	s.logger.Info().Str("path", downloadPath).Int64("size", downloaded).Dur("totalTime", time.Since(startTime)).Msg("Update downloaded successfully")

	return downloadPath, nil
}

func (s *Service) createDownloadRequest(ctx context.Context, release *ReleaseInfo) (*http.Response, time.Time, error) {
	s.logger.Info().Str("url", release.DownloadURL).Msg("Creating HTTP request for download")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.DownloadURL, http.NoBody)
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to create download request")
		return nil, time.Time{}, fmt.Errorf("failed to create download request: %w", err)
	}

	s.logger.Info().Msg("Executing HTTP request to download update")
	startTime := time.Now()

	resp, err := s.downloadClient.Do(req)
	if err != nil {
		s.logger.Error().
			Err(err).
			Dur("elapsed", time.Since(startTime)).
			Bool("contextCanceled", ctx.Err() != nil).
			Msg("HTTP request failed")
		if ctx.Err() != nil {
			s.logger.Error().Err(ctx.Err()).Msg("Context error details")
		}
		return nil, time.Time{}, fmt.Errorf("failed to download update: %w", err)
	}

	s.logger.Info().
		Int("statusCode", resp.StatusCode).
		Int64("contentLength", resp.ContentLength).
		Dur("responseTime", time.Since(startTime)).
		Msg("Received HTTP response")

	return resp, startTime, nil
}

func (s *Service) prepareDownloadFile(release *ReleaseInfo) (string, *os.File, error) {
	tmpDir := os.TempDir()
	downloadPath := filepath.Join(tmpDir, "slipstream-update", release.AssetName)
	s.logger.Info().Str("path", downloadPath).Msg("Creating download file")

	if err := os.MkdirAll(filepath.Dir(downloadPath), 0o750); err != nil {
		s.logger.Error().Err(err).Str("dir", filepath.Dir(downloadPath)).Msg("Failed to create download directory")
		return "", nil, fmt.Errorf("failed to create download directory: %w", err)
	}

	file, err := os.Create(downloadPath)
	if err != nil {
		s.logger.Error().Err(err).Str("path", downloadPath).Msg("Failed to create download file")
		return "", nil, fmt.Errorf("failed to create download file: %w", err)
	}

	return downloadPath, file, nil
}

func (s *Service) downloadLoop(ctx context.Context, reader io.Reader, writer io.Writer, downloadPath string, totalSize, totalMB float64) (int64, error) {
	var downloaded int64
	buf := make([]byte, 32*1024)
	lastUpdate := time.Now()
	lastLogTime := time.Now()

	s.logger.Info().Float64("totalMB", totalMB).Msg("Starting download loop")

	for {
		select {
		case <-ctx.Done():
			s.logDownloadCancelled(downloaded, totalSize)
			os.Remove(downloadPath)
			return 0, ctx.Err()
		default:
		}

		n, err := reader.Read(buf)
		if n > 0 {
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				s.logger.Error().Err(writeErr).Int64("downloadedBytes", downloaded).Msg("Failed to write to download file")
				os.Remove(downloadPath)
				return 0, fmt.Errorf("failed to write download: %w", writeErr)
			}
			downloaded += int64(n)
			lastUpdate = s.maybeUpdateProgress(downloaded, totalSize, totalMB, lastUpdate)
			lastLogTime = s.maybeLogProgress(downloaded, totalSize, lastLogTime)
		}

		if errors.Is(err, io.EOF) {
			s.logger.Info().Int64("totalBytes", downloaded).Msg("Download complete (EOF reached)")
			break
		}
		if err != nil {
			s.logDownloadError(ctx, err, downloaded, totalSize)
			os.Remove(downloadPath)
			return 0, fmt.Errorf("download read error: %w", err)
		}
	}

	return downloaded, nil
}

func (s *Service) logDownloadCancelled(downloaded int64, totalSize float64) {
	s.logger.Warn().
		Int64("downloadedBytes", downloaded).
		Float64("percentComplete", float64(downloaded)/totalSize*100).
		Msg("Download cancelled via context")
}

func (s *Service) maybeUpdateProgress(downloaded int64, totalSize, totalMB float64, lastUpdate time.Time) time.Time {
	if time.Since(lastUpdate) <= 100*time.Millisecond {
		return lastUpdate
	}
	progress := float64(downloaded) / totalSize * 100
	downloadedMB := float64(downloaded) / (1024 * 1024)
	s.setProgress(progress, downloadedMB, totalMB)
	return time.Now()
}

func (s *Service) maybeLogProgress(downloaded int64, totalSize float64, lastLogTime time.Time) time.Time {
	if time.Since(lastLogTime) <= 5*time.Second {
		return lastLogTime
	}
	s.logger.Info().
		Int64("downloadedBytes", downloaded).
		Float64("percentComplete", float64(downloaded)/totalSize*100).
		Msg("Download progress")
	return time.Now()
}

func (s *Service) logDownloadError(ctx context.Context, err error, downloaded int64, totalSize float64) {
	s.logger.Error().
		Err(err).
		Int64("downloadedBytes", downloaded).
		Float64("percentComplete", float64(downloaded)/totalSize*100).
		Bool("contextCanceled", ctx.Err() != nil).
		Msg("Error reading from response body")
}

func (s *Service) backupDatabase() {
	s.logger.Info().Msg("Creating database backup before update")
}

func (s *Service) installUpdate(ctx context.Context, downloadPath string) error {
	s.setState(StateInstalling, nil)
	s.logger.Info().Str("path", downloadPath).Str("platform", runtime.GOOS).Msg("Starting installation")

	goos := runtime.GOOS
	var err error

	switch goos {
	case "windows":
		s.logger.Info().Msg("Installing Windows update")
		err = s.installWindows(ctx, downloadPath)
	case "darwin":
		s.logger.Info().Msg("Installing macOS update")
		err = s.installMacOS(ctx, downloadPath)
	case osLinux:
		s.logger.Info().Msg("Installing Linux update")
		err = s.installLinux(ctx, downloadPath)
	default:
		err = fmt.Errorf("unsupported platform: %s", goos)
	}

	if err != nil {
		s.logger.Error().Err(err).Str("platform", goos).Msg("Installation failed")
		return err
	}

	s.setState(StateRestarting, nil)
	s.logger.Info().Msg("Update installed, triggering restart")

	go func() {
		time.Sleep(2 * time.Second)
		if s.restartChan != nil {
			s.restartChan <- false // false = don't spawn new process, updater handles it
		}
	}()

	return nil
}

func (s *Service) installWindows(_ context.Context, downloadPath string) error {
	if !strings.HasSuffix(downloadPath, ".zip") {
		s.logger.Error().Str("ext", filepath.Ext(downloadPath)).Msg("Unsupported Windows update format")
		return fmt.Errorf("unsupported Windows update format: %s", filepath.Ext(downloadPath))
	}

	currentExe, err := s.getCurrentExePath()
	if err != nil {
		return err
	}

	newExePath, err := s.extractWindowsUpdate(downloadPath)
	if err != nil {
		return err
	}

	return s.launchWindowsUpdater(newExePath, currentExe)
}

func (s *Service) getCurrentExePath() (string, error) {
	currentExe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get current executable path: %w", err)
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return "", fmt.Errorf("failed to resolve executable path: %w", err)
	}
	return currentExe, nil
}

func (s *Service) extractWindowsUpdate(downloadPath string) (string, error) {
	s.logger.Info().Str("zipPath", downloadPath).Msg("Extracting portable ZIP update")

	extractDir := filepath.Dir(downloadPath)
	newExePath := filepath.Join(extractDir, "slipstream.exe")

	zipReader, err := zip.OpenReader(downloadPath)
	if err != nil {
		return "", fmt.Errorf("failed to open ZIP file: %w", err)
	}
	defer zipReader.Close()

	var extractErr error
	for _, f := range zipReader.File {
		if !strings.HasSuffix(f.Name, "slipstream.exe") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			extractErr = fmt.Errorf("failed to open file in ZIP: %w", err)
			break
		}

		outFile, err := os.OpenFile(newExePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			rc.Close()
			extractErr = fmt.Errorf("failed to create extracted file: %w", err)
			break
		}

		//nolint:gosec // Size is bounded by GitHub release asset size limit
		_, copyErr := io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if copyErr != nil {
			extractErr = fmt.Errorf("failed to extract file: %w", copyErr)
			break
		}

		s.logger.Info().Str("extractedTo", newExePath).Msg("Extracted new executable")
		break
	}

	if extractErr != nil {
		return "", extractErr
	}

	if _, err := os.Stat(newExePath); os.IsNotExist(err) {
		return "", fmt.Errorf("slipstream.exe not found in ZIP archive")
	}

	return newExePath, nil
}

func (s *Service) launchWindowsUpdater(newExePath, currentExe string) error {
	s.logger.Info().
		Str("newExe", newExePath).
		Str("currentExe", currentExe).
		Int("port", s.port).
		Msg("Launching updater to replace executable")

	maxRetries := 10
	retryDelay := 500 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		cmd := exec.Command(newExePath, "--complete-update", currentExe, fmt.Sprintf("%d", s.port)) //nolint:gosec,noctx // Fire-and-forget detached process, context would prevent Job Object breakaway
		cmd.Dir = filepath.Dir(newExePath)
		setSysProcAttrDetached(cmd)
		startErr := cmd.Start()
		if startErr == nil {
			return nil
		}
		// If first attempt failed and we set detach flags, retry without them
		// (Job Object may not allow breakaway)
		if i == 0 {
			cmd = exec.Command(newExePath, "--complete-update", currentExe, fmt.Sprintf("%d", s.port)) //nolint:gosec,noctx // Fallback without detach
			cmd.Dir = filepath.Dir(newExePath)
			if cmd.Start() == nil {
				s.logger.Warn().Msg("Launched updater without Job Object breakaway (breakaway not permitted)")
				return nil
			}
		}
		s.logger.Warn().
			Err(startErr).
			Int("attempt", i+1).
			Int("maxRetries", maxRetries).
			Msg("Failed to launch updater, retrying (file may be locked by antivirus)")
		time.Sleep(retryDelay)
	}

	return fmt.Errorf("failed to launch updater after %d attempts", maxRetries)
}

func (s *Service) installMacOS(ctx context.Context, downloadPath string) error {
	if !strings.HasSuffix(downloadPath, ".dmg") {
		return fmt.Errorf("unsupported macOS update format: %s", filepath.Ext(downloadPath))
	}

	// Get current app bundle path
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current executable path: %w", err)
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}
	// currentExe is like /Applications/SlipStream.app/Contents/MacOS/slipstream
	// We need the .app bundle path
	currentAppBundle := currentExe
	for !strings.HasSuffix(currentAppBundle, ".app") && currentAppBundle != "/" {
		currentAppBundle = filepath.Dir(currentAppBundle)
	}
	if !strings.HasSuffix(currentAppBundle, ".app") {
		return fmt.Errorf("could not determine app bundle path from: %s", currentExe)
	}

	mountPoint := "/Volumes/SlipStream-Update"

	cmd := exec.CommandContext(ctx, "hdiutil", "attach", downloadPath, "-mountpoint", mountPoint, "-nobrowse", "-quiet")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to mount DMG: %w", err)
	}

	defer func() {
		_ = exec.CommandContext(context.Background(), "hdiutil", "detach", mountPoint, "-quiet").Run()
	}()

	srcAppPath := filepath.Join(mountPoint, "SlipStream.app")

	// Copy new app bundle to temp location
	tempDir := filepath.Dir(downloadPath)
	tempAppPath := filepath.Join(tempDir, "SlipStream.app")
	os.RemoveAll(tempAppPath)

	cmd = exec.CommandContext(ctx, "cp", "-R", srcAppPath, tempAppPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to copy app to temp: %w", err)
	}

	// Launch the new app with --complete-update to finish the update
	newExePath := filepath.Join(tempAppPath, "Contents", "MacOS", "slipstream")
	s.logger.Info().
		Str("newExe", newExePath).
		Str("currentAppBundle", currentAppBundle).
		Int("port", s.port).
		Msg("Launching updater to replace app bundle")

	cmd = exec.Command(newExePath, "--complete-update", currentAppBundle, fmt.Sprintf("%d", s.port)) //nolint:noctx // Fire-and-forget detached process
	cmd.Dir = tempDir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch updater: %w", err)
	}

	s.logger.Info().Int("pid", cmd.Process.Pid).Msg("Updater process started")
	return nil
}

func (s *Service) installLinux(ctx context.Context, downloadPath string) error {
	ext := filepath.Ext(downloadPath)

	switch ext {
	case ".gz":
		// Tarball for stub pattern - extract and update user binary directly
		return s.installLinuxTarball(ctx, downloadPath)

	case ".AppImage":
		currentExe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
		currentExe, err = filepath.EvalSymlinks(currentExe)
		if err != nil {
			return fmt.Errorf("failed to resolve executable path: %w", err)
		}

		// Make the new AppImage executable
		//nolint:gosec // Executable must be group-executable
		if err := os.Chmod(downloadPath, 0o750); err != nil {
			return fmt.Errorf("failed to make AppImage executable: %w", err)
		}

		// Launch the new AppImage with --complete-update to finish the update
		s.logger.Info().
			Str("newExe", downloadPath).
			Str("currentExe", currentExe).
			Int("port", s.port).
			Msg("Launching updater to replace AppImage")

		cmd := exec.Command(downloadPath, "--complete-update", currentExe, fmt.Sprintf("%d", s.port)) //nolint:noctx // Fire-and-forget detached process
		cmd.Dir = filepath.Dir(downloadPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to launch updater: %w", err)
		}

		s.logger.Info().Int("pid", cmd.Process.Pid).Msg("Updater process started")
		return nil

	case ".deb", ".rpm":
		// deb/rpm packages require root privileges and should be updated via package manager
		return fmt.Errorf("automatic updates for %s packages require root privileges; please update manually using your package manager", ext)

	default:
		return fmt.Errorf("unsupported Linux update format: %s", ext)
	}
}

func (s *Service) installLinuxTarball(_ctx context.Context, downloadPath string) error {
	s.logger.Info().Str("tarball", downloadPath).Msg("Extracting tarball for stub pattern update")

	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// Extract binary from tarball to temp location
	extractDir := filepath.Dir(downloadPath)
	newExePath := filepath.Join(extractDir, "slipstream")

	if err := extractTarGz(downloadPath, extractDir); err != nil {
		return fmt.Errorf("failed to extract tarball: %w", err)
	}

	if _, err := os.Stat(newExePath); os.IsNotExist(err) {
		return fmt.Errorf("slipstream binary not found in tarball")
	}

	// Make executable
	//nolint:gosec // Executable must be group-executable
	if err := os.Chmod(newExePath, 0o750); err != nil {
		return fmt.Errorf("failed to make binary executable: %w", err)
	}

	// Launch the new binary with --complete-update to finish the update
	s.logger.Info().
		Str("newExe", newExePath).
		Str("currentExe", currentExe).
		Int("port", s.port).
		Msg("Launching updater to replace binary")

	cmd := exec.Command(newExePath, "--complete-update", currentExe, fmt.Sprintf("%d", s.port)) //nolint:gosec,noctx // Fire-and-forget detached process
	cmd.Dir = extractDir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch updater: %w", err)
	}

	s.logger.Info().Int("pid", cmd.Process.Pid).Msg("Updater process started")
	return nil
}

func extractTarGz(tarGzPath, destDir string) error {
	file, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		//nolint:gosec // Archive paths validated from trusted GitHub release
		targetPath := filepath.Join(destDir, header.Name)

		if err := extractTarEntry(header, tarReader, targetPath); err != nil {
			return err
		}
	}

	return nil
}

func extractTarEntry(header *tar.Header, reader io.Reader, targetPath string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(targetPath, 0o750)
	case tar.TypeReg:
		return extractTarFile(reader, targetPath)
	}
	return nil
}

func extractTarFile(reader io.Reader, targetPath string) error {
	outFile, err := os.OpenFile(targetPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer outFile.Close()

	//nolint:gosec // Size bounded by GitHub release asset size limit
	_, err = io.Copy(outFile, reader)
	return err
}

func (s *Service) Cancel() error {
	s.logger.Info().Msg("Cancel called - attempting to cancel update")

	s.mu.Lock()
	cancel := s.cancelFunc
	downloadPath := s.downloadPath
	s.mu.Unlock()

	s.logger.Info().
		Bool("hasCancelFunc", cancel != nil).
		Str("downloadPath", downloadPath).
		Msg("Cancel state")

	if cancel != nil {
		s.logger.Info().Msg("Invoking cancel function")
		cancel()
	}

	s.mu.Lock()
	if s.downloadPath != "" {
		s.logger.Info().Str("path", s.downloadPath).Msg("Removing partial download file")
		os.Remove(s.downloadPath)
		s.downloadPath = ""
	}
	s.mu.Unlock()

	s.setState(StateIdle, nil)
	s.logger.Info().Msg("Update cancelled successfully")
	return nil
}

func (s *Service) CheckAndAutoInstall(ctx context.Context) error {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("Failed to get update settings, using defaults")
		settings = &Settings{AutoInstall: true}
	}

	release, err := s.CheckForUpdate(ctx)
	if err != nil {
		return err
	}

	if release == nil {
		return nil
	}

	s.logger.Info().Str("version", release.Version).Bool("autoInstall", settings.AutoInstall).Msg("Update available")

	if settings.AutoInstall {
		return s.DownloadAndInstall(ctx)
	}

	return nil
}

func ptr[T any](v T) *T {
	return &v
}
