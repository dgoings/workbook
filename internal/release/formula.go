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

// RenderFormula produces the macOS-only Homebrew formula for a Workbook
// release. The checksums must be SHA-256 values for the named release archives.
func RenderFormula(version, arm64SHA, amd64SHA, repository string) (string, error) {
	if !semanticVersion.MatchString(version) {
		return "", fmt.Errorf("release version %q must be MAJOR.MINOR.PATCH without leading zeroes", version)
	}
	if !repositoryName.MatchString(repository) {
		return "", fmt.Errorf("repository %q must be an owner/name pair", repository)
	}
	arm64SHA, err := normalizedSHA256("arm64", arm64SHA)
	if err != nil {
		return "", err
	}
	amd64SHA, err = normalizedSHA256("amd64", amd64SHA)
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
  depends_on :macos

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
    assert_match version, shell_output("#{bin}/workbook version")
  end
end
`, repository, version, arm64SHA, amd64SHA), nil
}

func normalizedSHA256(architecture, value string) (string, error) {
	if len(value) != 64 {
		return "", fmt.Errorf("%s checksum must be a 64-character SHA-256 value", architecture)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("%s checksum must be hexadecimal: %w", architecture, err)
	}
	return strings.ToLower(value), nil
}
