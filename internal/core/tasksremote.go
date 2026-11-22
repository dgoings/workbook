package core

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// tasksRemoteNamePattern is the remote names a project may choose for its
// tasks: what Git accepts as a remote name, narrowed to one path component so
// the tracking namespace refs/workbook/remotes/<name>/tasks/* can never be read
// two ways.
var tasksRemoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ValidateTasksRemoteName refuses a name that is not a single-component Git
// remote name.
func ValidateTasksRemoteName(name string) error {
	if !tasksRemoteNamePattern.MatchString(name) || strings.Contains(name, "..") ||
		strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") {
		return Errorf(CategoryValidation,
			"%q is not a Git remote name Workbook can use for tasks: use letters, digits, '.', '_' and '-', "+
				"starting with a letter, digit or '_'", name)
	}
	return nil
}

// ValidateTasksRemoteURL refuses an address that cannot be written into the
// project's committed settings.
//
// The file travels with the code to everybody who clones it, so an address
// carrying a password or a token would publish it. An SSH user name such as
// the `git` in git@github.com:team/tasks.git is not a credential and is kept;
// any user name in an http or https address is refused, because a token is
// commonly passed that way.
func ValidateTasksRemoteURL(address string) error {
	refuse := func(reason string) error {
		return Errorf(CategoryValidation, "%q cannot be this project's tasks address: %s", address, reason)
	}
	if strings.TrimSpace(address) == "" {
		return Errorf(CategoryValidation, "the tasks remote needs an address")
	}
	if strings.TrimSpace(address) != address {
		return refuse("it begins or ends with white space")
	}
	for _, character := range address {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return refuse("it contains white space or a control character")
		}
	}
	if strings.HasPrefix(address, "-") {
		return refuse("it begins with '-', which Git would read as an option")
	}
	if strings.Contains(address, "://") {
		parsed, err := url.Parse(address)
		if err != nil {
			return refuse("it is not an address")
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return refuse("it carries a password; give the address alone and let Git's credential helper supply it")
			}
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https":
				return refuse("it carries a user name, which is how a token is usually passed; give the address alone and let Git's credential helper supply it")
			}
		}
		return nil
	}
	// The scp-like form user@host:path has no place for a password; a colon
	// after the user name would be read as the host separator by Git.
	return nil
}
