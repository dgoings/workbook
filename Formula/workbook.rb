# frozen_string_literal: true

# Homebrew formula for Workbook.
class Workbook < Formula
  desc "Repository-native project tracker for humans and coding agents"
  homepage "https://github.com/dgoings/workbook"
  version "0.1.0"

  on_macos do
    on_arm do
      url "https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end

    on_intel do
      url "https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    bin.install "workbook"
  end

  test do
    assert_match version, shell_output("#{bin}/workbook version")
  end
end
