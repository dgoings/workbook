package release

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var (
	repositoryName  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// FormulaArchives holds the SHA-256 checksum of each published release archive
// the formula can serve. Every platform Homebrew supports must be present, so a
// missing build fails rendering instead of shipping a formula that cannot
// install on one of them.
type FormulaArchives struct {
	DarwinARM64 string
	DarwinAMD64 string
	LinuxARM64  string
	LinuxAMD64  string
}

// RenderFormula produces the Homebrew formula for a Workbook release. The
// formula serves macOS and Linux, on both arm64 and amd64.
func RenderFormula(version, repository string, archives FormulaArchives) (string, error) {
	if !semanticVersion.MatchString(version) {
		return "", fmt.Errorf("release version %q must be MAJOR.MINOR.PATCH without leading zeroes", version)
	}
	if !repositoryName.MatchString(repository) {
		return "", fmt.Errorf("repository %q must be an owner/name pair", repository)
	}
	darwinARM64, err := normalizedSHA256("darwin arm64", archives.DarwinARM64)
	if err != nil {
		return "", err
	}
	darwinAMD64, err := normalizedSHA256("darwin amd64", archives.DarwinAMD64)
	if err != nil {
		return "", err
	}
	linuxARM64, err := normalizedSHA256("linux arm64", archives.LinuxARM64)
	if err != nil {
		return "", err
	}
	linuxAMD64, err := normalizedSHA256("linux amd64", archives.LinuxAMD64)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`# typed: strict
# frozen_string_literal: true

# Homebrew formula for Workbook.
class Workbook < Formula
  desc "Repository-native project tracker for humans and coding agents"
  homepage "https://github.com/%[1]s"
  version "%[2]s"

  on_macos do
    on_arm do
      url "https://github.com/%[1]s/releases/download/v%[2]s/workbook_%[2]s_darwin_arm64.tar.gz"
      sha256 "%[3]s"
    end

    on_intel do
      url "https://github.com/%[1]s/releases/download/v%[2]s/workbook_%[2]s_darwin_amd64.tar.gz"
      sha256 "%[4]s"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/%[1]s/releases/download/v%[2]s/workbook_%[2]s_linux_arm64.tar.gz"
      sha256 "%[5]s"
    end

    on_intel do
      url "https://github.com/%[1]s/releases/download/v%[2]s/workbook_%[2]s_linux_amd64.tar.gz"
      sha256 "%[6]s"
    end
  end

  def install
    bin.install "workbook"
  end

  def caveats
    <<~CAVEATS
      Workbook generates agent documentation per project, so upgrading this
      formula cannot refresh the projects on your machine.

      Run "workbook setup" in each project that uses Workbook to refresh its
      managed documentation, and "workbook docs status" to check whether a
      project is current.
    CAVEATS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/workbook version")
  end
end
`, repository, version, darwinARM64, darwinAMD64, linuxARM64, linuxAMD64), nil
}

func normalizedSHA256(platform, value string) (string, error) {
	if len(value) != 64 {
		return "", fmt.Errorf("%s checksum must be a 64-character SHA-256 value", platform)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("%s checksum must be hexadecimal: %w", platform, err)
	}
	return strings.ToLower(value), nil
}
