package git

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// StatusArgs are the arguments of the git status that ParseStatus reads: the
// branch, its upstream and how far apart they are, and every changed path, untracked
// ones included, in one process. --ahead-behind, or status.aheadBehind=false
// would print "+? -?" for a branch that differs from its upstream.
var StatusArgs = []string{"status", "--porcelain=v2", "--branch", "--ahead-behind", "-z", "--untracked-files=normal"}

// Status is a working tree as git status sees it.
type Status struct {
	// Commit is HEAD's commit, empty before the first one.
	Commit string
	// Branch is the branch checked out, empty when HEAD is detached.
	Branch   string
	Upstream string
	// Compared is whether Ahead and Behind were counted: a branch whose upstream
	// is gone has an upstream but nothing to compare with.
	Compared      bool
	Ahead, Behind int
	// Paths are the paths with changes, staged or not, and those untracked. An
	// untracked directory is one path ending in "/".
	Paths []string
	// Conflicted counts the paths left unmerged.
	Conflicted int
}

// ParseStatus reads what StatusArgs prints.
func ParseStatus(out string) (Status, error) {
	var status Status
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}
		switch record[0] {
		case '#':
			header, value, _ := strings.Cut(strings.TrimPrefix(record, "# "), " ")
			switch header {
			case "branch.oid":
				if value != "(initial)" {
					status.Commit = value
				}
			case "branch.head":
				if value != "(detached)" {
					status.Branch = value
				}
			case "branch.upstream":
				status.Upstream = value
			case "branch.ab":
				if _, err := fmt.Sscanf(value, "+%d -%d", &status.Ahead, &status.Behind); err != nil {
					return Status{}, errors.New("git status printed an ahead and behind count it could not read")
				}
				status.Compared = true
			}
		case '1':
			path, err := field(record, 9)
			if err != nil {
				return Status{}, err
			}
			status.Paths = append(status.Paths, path)
		case '2':
			path, err := field(record, 10)
			if err != nil {
				return Status{}, err
			}
			status.Paths = append(status.Paths, path)
			// A rename's or copy's original path is the next record.
			if i+1 < len(records) {
				i++
				status.Paths = append(status.Paths, records[i])
			}
		case 'u':
			path, err := field(record, 11)
			if err != nil {
				return Status{}, err
			}
			status.Paths = append(status.Paths, path)
			status.Conflicted++
		case '?':
			status.Paths = append(status.Paths, strings.TrimPrefix(record, "? "))
		}
	}
	return status, nil
}

// field returns the last of n space-separated fields, which is a path and may
// hold spaces itself.
func field(record string, n int) (string, error) {
	fields := strings.SplitN(record, " ", n)
	if len(fields) != n {
		return "", errors.New("git status printed an entry it could not read")
	}
	return fields[n-1], nil
}

// Overlapping returns the paths of local that incoming also touches: the same
// path, or one beneath the other, as an untracked directory is listed whole.
// Changes stashed before a fast-forward apply back cleanly when none overlap.
func Overlapping(local, incoming []string) []string {
	var overlapping []string
	for _, l := range local {
		l = strings.TrimSuffix(l, "/")
		for _, in := range incoming {
			in = strings.TrimSuffix(in, "/")
			if l == in || strings.HasPrefix(in, l+"/") || strings.HasPrefix(l, in+"/") {
				overlapping = append(overlapping, l)
				break
			}
		}
	}
	return overlapping
}

// SplitNUL splits output terminated by NULs, as -z prints it.
func SplitNUL(out string) []string {
	var parts []string
	for _, part := range strings.Split(out, "\x00") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// Lines splits output into its non-empty lines.
func Lines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// RefFormat is the --format of the git for-each-ref that ParseRefs reads.
const RefFormat = "%(refname)%00%(objectname)%00%(committerdate:unix)%00%(upstream)%00%(symref)" +
	"%00%(upstream:remotename)%00%(upstream:remoteref)"

// Ref is a branch, local or remote-tracking, as for-each-ref lists it.
type Ref struct {
	// Ref is the full name: refs/heads/main, refs/remotes/origin/main.
	Ref    string
	Commit string
	// Date is when the tip was committed.
	Date time.Time
	// Upstream is the remote-tracking ref a local branch's upstream is read into;
	// UpstreamRemote and UpstreamRef are that upstream as the remote has it:
	// origin, refs/heads/main.
	Upstream, UpstreamRemote, UpstreamRef string
	// Symref is the ref this one is an alias of, as origin/HEAD is of the default
	// branch; empty for a branch of its own.
	Symref string
}

// RemoteHead reads the branch git ls-remote --symref <remote> HEAD says the
// remote's HEAD is, which is its default branch.
func RemoteHead(out string) (string, bool) {
	branch, ok := strings.CutPrefix(RemoteSymrefs(out)["HEAD"], "refs/heads/")
	return branch, ok && branch != ""
}

// RemoteTips reads every ref git ls-remote lists on the remote -- branches,
// tags, a tag peeled (refs/tags/v1^{}), pull requests' -- and the object each
// names, by full name.
func RemoteTips(out string) map[string]string {
	tips := make(map[string]string)
	for _, line := range Lines(out) {
		object, ref, ok := strings.Cut(line, "\t")
		if ok && !strings.HasPrefix(object, "ref: ") {
			tips[ref] = object
		}
	}
	return tips
}

// Shadowed are the branches of deleting whose commit HEAD or another branch kept
// on the remote is also at, which is how an alias of a branch shows: deleting
// one on the remote deletes the branch it names. Tags do not count -- a branch at
// a tagged commit, as a release branch at its release is, loses nothing the tag
// does not keep -- nor do the refs a host keeps for itself, such as a pull
// request's refs/pull/12/head, which sits at its branch's tip.
func Shadowed(tips map[string]string, deleting []string) map[string]bool {
	gone := make(map[string]bool, len(deleting))
	for _, name := range deleting {
		gone["refs/heads/"+name] = true
	}
	kept := make(map[string]bool)
	for ref, object := range tips {
		if (ref == "HEAD" || strings.HasPrefix(ref, "refs/heads/")) && !gone[ref] {
			kept[object] = true
		}
	}
	shadowed := make(map[string]bool)
	for _, name := range deleting {
		if object, ok := tips["refs/heads/"+name]; ok && kept[object] {
			shadowed[name] = true
		}
	}
	return shadowed
}

// Records splits what git printed one value a line, keeping each as git
// printed it: a path's trailing space is part of the path.
func Records(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// RemoteSymrefs reads the aliases git ls-remote --symref lists on the remote, by
// name: HEAD, and any branch that is another's alias. Deleting an alias on a
// remote deletes the branch it names.
func RemoteSymrefs(out string) map[string]string {
	symrefs := make(map[string]string)
	for _, line := range Lines(out) {
		rest, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		if target, name, ok := strings.Cut(rest, "\t"); ok {
			symrefs[name] = target
		}
	}
	return symrefs
}

// ParseRefs reads what for-each-ref prints with RefFormat.
func ParseRefs(out string) ([]Ref, error) {
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 7 {
			return nil, errors.New("git for-each-ref printed a line it could not read")
		}
		// No commit, no date: an annotated tag a refspec fetches among the
		// branches, which is no branch.
		if fields[2] == "" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, errors.New("git for-each-ref printed a date it could not read")
		}
		refs = append(refs, Ref{Ref: fields[0], Commit: fields[1], Date: time.Unix(seconds, 0), Upstream: fields[3],
			Symref: fields[4], UpstreamRemote: fields[5], UpstreamRef: fields[6]})
	}
	return refs, nil
}

// Stale returns the refs last committed to before cutoff, but for those keep
// names, and aliases: an alias has no age of its own, and deleting one through
// git would delete the branch it names.
func Stale(refs []Ref, cutoff time.Time, keep func(Ref) bool) []Ref {
	var stale []Ref
	for _, ref := range refs {
		if ref.Date.Before(cutoff) && ref.Symref == "" && !keep(ref) {
			stale = append(stale, ref)
		}
	}
	return stale
}

// Source is the branch on the remote a remote-tracking ref was fetched from,
// read back through the remote's fetch refspecs: refs/remotes/origin/feature is
// feature with the usual one. It is false for a ref fetched from anything but a
// branch, such as refs/remotes/origin/pr/12 from refs/pull/12/head, and for one
// two refspecs could have fetched, which may be either.
func Source(refspecs []string, tracking string) (string, bool) {
	found, matches := "", 0
	for _, refspec := range refspecs {
		refspec = strings.TrimPrefix(refspec, "+")
		if strings.HasPrefix(refspec, "^") {
			continue
		}
		src, dst, ok := strings.Cut(refspec, ":")
		if !ok {
			continue
		}
		source := ""
		if before, after, glob := strings.Cut(dst, "*"); glob {
			middle, matched := strings.CutPrefix(tracking, before)
			if !matched || !strings.HasSuffix(middle, after) || len(middle) < len(after) {
				continue
			}
			source = strings.Replace(src, "*", strings.TrimSuffix(middle, after), 1)
		} else if dst == tracking {
			source = src
		} else {
			continue
		}
		matches++
		found = source
	}
	name, ok := strings.CutPrefix(found, "refs/heads/")
	if matches != 1 || !ok || name == "" {
		return "", false
	}
	return name, true
}

// Destinations are where refspecs fetch into, as for-each-ref patterns:
// refs/remotes/origin/ for the usual one, refs/remotes/mirror/ for another. A
// pattern matches whole path components, so one whose wildcard is within a
// component -- refs/remotes/origin/archive-* -- is read from the directory it is
// in, and Source tells its refs apart. One into refs/heads or refs/tags is left
// out: what is there is a local branch or a tag, whatever fetched it.
func Destinations(refspecs []string) []string {
	var patterns []string
	for _, refspec := range refspecs {
		refspec = strings.TrimPrefix(refspec, "+")
		if strings.HasPrefix(refspec, "^") {
			continue
		}
		_, dst, ok := strings.Cut(refspec, ":")
		if !ok || dst == "" || strings.HasPrefix(dst, "refs/heads/") || strings.HasPrefix(dst, "refs/tags/") {
			continue
		}
		if before, _, glob := strings.Cut(dst, "*"); glob {
			dst = before[:strings.LastIndex(before, "/")+1]
		}
		if dst != "" && !slices.Contains(patterns, dst) {
			patterns = append(patterns, dst)
		}
	}
	return patterns
}

// Pushed is what a push did to one ref, as --porcelain reports it.
type Pushed struct {
	Done bool
	// Reason is why it was not done: "remote rejected (protected branch hook declined)".
	Reason string
}

var brackets = strings.NewReplacer("[", "", "]", "")

// ParsePush reads what git push --porcelain prints, by the remote ref each line
// is about.
func ParsePush(out string) map[string]Pushed {
	pushed := make(map[string]Pushed)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 || len(fields[0]) != 1 {
			continue
		}
		_, to, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		switch fields[0] {
		case "-", " ", "+", "*", "=":
			pushed[to] = Pushed{Done: true}
		default:
			pushed[to] = Pushed{Reason: brackets.Replace(strings.TrimSpace(fields[2]))}
		}
	}
	return pushed
}

// ObjectsBytes reads what git count-objects -v prints into the bytes the
// repository's objects take: loose, packed and garbage.
func ObjectsBytes(out string) (int64, error) {
	var total int64
	for _, line := range Lines(out) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "size", "size-pack", "size-garbage":
			kib, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return 0, errors.New("git count-objects printed a size it could not read")
			}
			total += kib << 10
		}
	}
	return total, nil
}

// Cleaned reads the paths git clean removed, or would remove with -n. A path
// git quoted, as it does one holding a quote, a backslash or a control
// character even with core.quotePath off, is read back as it is on disk.
func Cleaned(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		for _, prefix := range []string{"Would remove ", "Removing "} {
			if path, ok := strings.CutPrefix(line, prefix); ok {
				paths = append(paths, unquote(path))
				break
			}
		}
	}
	return paths
}

// unquote reads a path git quoted C-style back into the bytes it names. It is
// not strconv.Unquote, which takes a byte that is no UTF-8 -- a file named in
// Latin-1 -- for U+FFFD, and so names another file.
func unquote(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	quoted := path[1 : len(path)-1]
	escapes := map[byte]byte{'a': '\a', 'b': '\b', 't': '\t', 'n': '\n', 'v': '\v', 'f': '\f', 'r': '\r', '"': '"', '\\': '\\'}
	var b strings.Builder
	for i := 0; i < len(quoted); i++ {
		if quoted[i] != '\\' || i+1 == len(quoted) {
			b.WriteByte(quoted[i])
			continue
		}
		i++
		if c, ok := escapes[quoted[i]]; ok {
			b.WriteByte(c)
			continue
		}
		if i+2 < len(quoted) {
			if octal, err := strconv.ParseUint(quoted[i:i+3], 8, 8); err == nil {
				b.WriteByte(byte(octal))
				i += 2
				continue
			}
		}
		// Not an escape git writes: kept as it is.
		b.WriteByte('\\')
		b.WriteByte(quoted[i])
	}
	return b.String()
}

// Literal makes a path a pathspec that names that path alone: without it, a
// directory called cache* would also name cache-prod, and one starting with
// ":(" would be read as pathspec magic.
func Literal(path string) string { return ":(literal)" + path }
