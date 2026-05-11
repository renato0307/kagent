package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionSRTSettingsFileName is the filename SRT settings are written to
// inside each session directory. CommandExecutor prefers this file (when
// present) over the pod-wide KAGENT_SRT_SETTINGS_PATH so the sandbox policy
// can be scoped to the session.
const SessionSRTSettingsFileName = "srt-settings.json"

// Skill represents a discovered skill with metadata
type Skill struct {
	Name        string
	Description string
}

// DiscoverSkills discovers available skills in the skills directory
func DiscoverSkills(skillsDirectory string) ([]Skill, error) {
	if skillsDirectory == "" {
		return []Skill{}, nil
	}
	dir := filepath.Clean(skillsDirectory)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []Skill{}, nil
	}

	var skills []Skill
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read skills directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(dir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")

		if _, err := os.Stat(skillFile); os.IsNotExist(err) {
			continue
		}

		// Parse skill metadata from SKILL.md
		metadata, err := parseSkillMetadata(skillFile)
		if err != nil {
			continue // Skip skills with invalid metadata
		}

		skills = append(skills, Skill{
			Name:        metadata["name"],
			Description: metadata["description"],
		})
	}

	return skills, nil
}

// LoadSkillContent loads the full content of a skill's SKILL.md file
func LoadSkillContent(skillsDirectory, skillName string) (string, error) {
	skillDir := filepath.Join(skillsDirectory, skillName)
	skillFile := filepath.Join(skillDir, "SKILL.md")

	if _, err := os.Stat(skillFile); os.IsNotExist(err) {
		return "", fmt.Errorf("skill '%s' not found or has no SKILL.md file", skillName)
	}

	content, err := os.ReadFile(skillFile)
	if err != nil {
		return "", fmt.Errorf("failed to load skill '%s': %w", skillName, err)
	}

	return string(content), nil
}

// parseSkillMetadata parses YAML frontmatter from SKILL.md
func parseSkillMetadata(skillFile string) (map[string]string, error) {
	content, err := os.ReadFile(skillFile)
	if err != nil {
		return nil, err
	}

	contentStr := string(content)
	if !strings.HasPrefix(contentStr, "---") {
		return nil, fmt.Errorf("no YAML frontmatter found")
	}

	parts := strings.SplitN(contentStr, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid YAML frontmatter format")
	}

	// Simple YAML parsing for name and description
	// For full YAML support, you might want to use a YAML library
	frontmatter := parts[1]
	metadata := make(map[string]string)

	lines := strings.SplitSeq(frontmatter, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "name:"); ok {
			metadata["name"] = strings.TrimSpace(after)
			metadata["name"] = strings.Trim(metadata["name"], `"'`)
		} else if after, ok := strings.CutPrefix(line, "description:"); ok {
			metadata["description"] = strings.TrimSpace(after)
			metadata["description"] = strings.Trim(metadata["description"], `"'`)
		}
	}

	if metadata["name"] == "" || metadata["description"] == "" {
		return nil, fmt.Errorf("missing required metadata fields")
	}

	return metadata, nil
}

// GenerateSkillsToolDescription generates a tool description with available skills
func GenerateSkillsToolDescription(skills []Skill) string {
	if len(skills) == 0 {
		return "No skills available. Use this tool to discover and load skill instructions."
	}

	var desc strings.Builder
	desc.WriteString("Discover and load skill instructions. Available skills:\n\n")

	for _, skill := range skills {
		fmt.Fprintf(&desc, "- %s: %s\n", skill.Name, skill.Description)
	}

	desc.WriteString("\nCall this tool with command='<skill-name>' to load the full skill instructions.")
	return desc.String()
}

// GetSessionPath returns the working directory path for a session
func GetSessionPath(sessionID, skillsDirectory string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("sessionID cannot be empty")
	}

	basePath := filepath.Join(os.TempDir(), "kagent")
	sessionPath := filepath.Clean(filepath.Join(basePath, sessionID))

	// Validate the resolved path stays under basePath to prevent path traversal
	if !strings.HasPrefix(sessionPath, filepath.Clean(basePath)+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid sessionID: path traversal detected")
	}

	// Create working directories
	uploadsDir := filepath.Join(sessionPath, "uploads")
	outputsDir := filepath.Join(sessionPath, "outputs")

	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create uploads directory: %w", err)
	}
	if err := os.MkdirAll(outputsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create outputs directory: %w", err)
	}

	// Create symlink to skills directory
	skillsLink := filepath.Join(sessionPath, "skills")
	// Use absolute path for symlink target to avoid issues with relative paths
	absSkillsDir, err := filepath.Abs(skillsDirectory)
	if err != nil {
		// If we can't get absolute path, use original
		absSkillsDir = skillsDirectory
	}

	// Check if symlink already exists
	if linkInfo, err := os.Lstat(skillsLink); err == nil {
		// If it's a symlink, check if it points to the correct location
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			existingTarget, err := os.Readlink(skillsLink)
			if err == nil {
				// Resolve existing target to absolute path
				var absExistingTarget string
				if filepath.IsAbs(existingTarget) {
					absExistingTarget, _ = filepath.Abs(existingTarget)
				} else {
					absExistingTarget = filepath.Join(filepath.Dir(skillsLink), existingTarget)
					absExistingTarget, _ = filepath.Abs(absExistingTarget)
				}
				absExistingTarget = filepath.Clean(absExistingTarget)
				absSkillsDirClean := filepath.Clean(absSkillsDir)

				// If it points to the correct location, we're done
				if absExistingTarget == absSkillsDirClean {
					return sessionPath, nil
				}
			}
		}
		// Remove existing symlink/file if it doesn't point to the correct location
		os.Remove(skillsLink)
	}

	// Create new symlink
	if err := os.Symlink(absSkillsDir, skillsLink); err != nil {
		// Ignore: skills can still be accessed via absolute path
		_ = err
	}

	if err := writeSessionSRTSettings(sessionPath); err != nil {
		// Non-fatal: CommandExecutor falls back to the pod-wide settings file
		// (KAGENT_SRT_SETTINGS_PATH) when the session-scoped file is absent.
		// Log nothing here — callers own the context for observability.
		_ = err
	}

	return sessionPath, nil
}

// writeSessionSRTSettings materializes a per-session SRT settings file under
// sessionPath. It reads the pod-wide base settings from KAGENT_SRT_SETTINGS_PATH
// (written by the kagent controller into a mounted Secret) and narrows the
// filesystem allowWrite list to the session's own directory so that bash /
// python tool invocations in one session cannot write into another session's
// uploads/outputs directories.
func writeSessionSRTSettings(sessionPath string) error {
	basePath := strings.TrimSpace(os.Getenv("KAGENT_SRT_SETTINGS_PATH"))
	if basePath == "" {
		return fmt.Errorf("KAGENT_SRT_SETTINGS_PATH is not set")
	}
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("failed to read base SRT settings %s: %w", basePath, err)
	}

	var settings map[string]any
	if err := json.Unmarshal(baseBytes, &settings); err != nil {
		return fmt.Errorf("failed to parse base SRT settings %s: %w", basePath, err)
	}

	fs, ok := settings["filesystem"].(map[string]any)
	if !ok {
		fs = map[string]any{}
		settings["filesystem"] = fs
	}
	// Scope writes to this session only. "." covers the process cwd (also the
	// session dir); the absolute path makes absolute-path writes explicit.
	fs["allowWrite"] = []string{".", sessionPath}
	// Hide sibling session directories. SRT tmpfs-masks denyRead paths first
	// and then applies allowWrite bind-mounts, so this session's own dir
	// reappears on top of the mask. Net effect: attacker sees its own session
	// but not /tmp/kagent/<other-session>/*.
	sessionsRoot := filepath.Dir(sessionPath)
	existingDenyRead, _ := fs["denyRead"].([]any)
	denyRead := make([]string, 0, len(existingDenyRead)+1)
	for _, v := range existingDenyRead {
		if s, ok := v.(string); ok && s != sessionsRoot {
			denyRead = append(denyRead, s)
		}
	}
	denyRead = append(denyRead, sessionsRoot)
	fs["denyRead"] = denyRead

	out, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal session SRT settings: %w", err)
	}

	sessionSettingsPath := filepath.Join(sessionPath, SessionSRTSettingsFileName)
	if err := os.WriteFile(sessionSettingsPath, out, 0600); err != nil {
		return fmt.Errorf("failed to write session SRT settings: %w", err)
	}
	return nil
}
