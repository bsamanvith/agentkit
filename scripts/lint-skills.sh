#!/usr/bin/env bash
# Lints the agent skills and the task files (docs/ENGINEERING.md §4.3 and §5).
# Run by make lint.
#
#   scripts/lint-skills.sh [root]     lint the repo (or the tree at root)
#   scripts/lint-skills.sh --test     run it on the fixtures in
#                                     scripts/testdata/skills: good/ must pass,
#                                     each case must fail with its check
#                                     (failures on stderr, ok lines on stdout)
#
# Checks, by ID (each failure line starts with its ID):
#   symlink.claude     .claude/skills is a symlink to ../.agents/skills
#   symlink.broken     no broken symlink under .agents or .claude
#   skill.file         every directory in .agents/skills has a SKILL.md
#   skill.frontmatter  SKILL.md starts with front matter (--- ... ---)
#   skill.name         its name equals the directory name
#   skill.description  it has a non-empty description
#   skill.length       its body is at most SKILL_MAX_LINES lines (default 150)
#   link.file          every relative link in AGENTS.md, CLAUDE.md, the
#                      skills and tasks/ points at a file that exists
#   link.anchor        and every #anchor into a Markdown file is a heading there
#   task.id            task and epic ids are unique and match the file name
#   task.status        status is todo, in-progress, review, done or blocked
#   task.depends       every depends_on names an existing task, or one an
#                      epic lists as planned ("- [ ] T079 ...", no file yet)
#   epic.checklist     an epic's box for its own task is ticked when the task
#                      is done, and empty when it is todo, in-progress or
#                      blocked (either while it is in review)
#
# Portable: bash 3.2 and BSD awk (macOS) as well as Linux.
set -uo pipefail
export LC_ALL=C

max_lines="${SKILL_MAX_LINES:-150}"
here="$(cd "$(dirname "$0")" && pwd)"

# self_test runs the linter on each fixture in a scratch copy of good/.
self_test() {
	local fixtures="$here/testdata/skills" fails=0 c want out
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	# make_tree DIR [CASE]: good/ plus the case's files, with agents/ as
	# .agents/ (so no agent loads the fixtures' skills) and the symlink.
	make_tree() {
		mkdir -p "$1"
		cp -R "$fixtures/good/." "$1/"
		if [ -n "${2:-}" ]; then
			(cd "$2" && find . -type f ! -name expect ! -name setup.sh -o -type l) |
				while read -r f; do
					mkdir -p "$1/$(dirname "$f")"
					cp -RP "$2/$f" "$1/$f"
				done
		fi
		mv "$1/agents" "$1/.agents"
		mkdir -p "$1/.claude"
		ln -s ../.agents/skills "$1/.claude/skills"
		if [ -n "${2:-}" ] && [ -f "$2/setup.sh" ]; then
			(cd "$1" && sh "$2/setup.sh")
		fi
	}
	make_tree "$tmp/good"
	if ! out="$("$0" "$tmp/good" 2>&1)"; then
		echo "FAIL good: $out" >&2
		fails=$((fails + 1))
	else
		echo "ok   good"
	fi
	for c in "$fixtures"/cases/*/; do
		c="${c%/}"
		want="$(cat "$c/expect")"
		make_tree "$tmp/$(basename "$c")" "$c"
		if out="$("$0" "$tmp/$(basename "$c")" 2>&1)"; then
			echo "FAIL $(basename "$c"): passed, want $want" >&2
			fails=$((fails + 1))
		elif ! printf '%s\n' "$out" | grep -q "^$want " ||
			printf '%s\n' "$out" | grep -v "^$want " | grep -qv '^lint-skills: '; then
			# Only the case's own check may fire: a case that fails for
			# another reason proves nothing about its check.
			{
				echo "FAIL $(basename "$c"): want only $want, got:"
				printf '%s\n' "$out" | sed 's/^/       /'
			} >&2
			fails=$((fails + 1))
		else
			echo "ok   $(basename "$c") ($want)"
		fi
	done
	[ "$fails" -eq 0 ]
}

if [ "${1:-}" = --test ]; then
	self_test
	exit
fi

root="${1:-$here/..}"
root="$(cd "$root" && pwd)" || exit 2
cd "$root" || exit 2
errors=0
fail() { # fail ID WHERE MESSAGE
	echo "$1 $2: $3" >&2
	errors=$((errors + 1))
}
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/anchors"

# --- symlinks ---------------------------------------------------------------

if [ ! -L .claude/skills ]; then
	fail symlink.claude .claude/skills "is not a symlink (want ../.agents/skills)"
elif [ "$(readlink .claude/skills)" != ../.agents/skills ]; then
	fail symlink.claude .claude/skills "points at $(readlink .claude/skills), want ../.agents/skills"
fi
for d in .agents .claude; do
	[ -e "$d" ] || [ -L "$d" ] || continue
	find "$d" -type l | while read -r l; do
		[ -e "$l" ] || echo "$l"
	done
done >"$work/broken"
while read -r l; do
	fail symlink.broken "$l" "points at $(readlink "$l"), which does not exist"
done <"$work/broken"

# --- skills -----------------------------------------------------------------

# frontmatter FILE KEY prints KEY's value from FILE's front matter.
frontmatter() {
	awk -v key="$2" '
		NR == 1 && $0 != "---" { exit }
		NR > 1 && $0 == "---" { exit }
		NR > 1 && index($0, key ":") == 1 {
			v = substr($0, length(key) + 2)
			sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
			print v; exit
		}' "$1"
}

for dir in .agents/skills/*/; do
	[ -d "$dir" ] || continue
	dir="${dir%/}"
	name="$(basename "$dir")"
	f="$dir/SKILL.md"
	if [ ! -f "$f" ]; then
		fail skill.file "$dir" "has no SKILL.md"
		continue
	fi
	# Line numbers of the front matter's two fences (0 when missing).
	fences="$(awk '$0 == "---" { n++; printf "%d ", NR; if (n == 2) exit }' "$f")"
	set -- $fences
	if [ "${1:-0}" != 1 ] || [ -z "${2:-}" ]; then
		fail skill.frontmatter "$f" "must start with front matter between --- lines"
		continue
	fi
	end="$2"
	got="$(frontmatter "$f" name)"
	[ "$got" = "$name" ] || fail skill.name "$f" "name is \"$got\", want the directory name \"$name\""
	desc="$(frontmatter "$f" description)"
	case "$desc" in
	"" | ">" | "|" | ">-" | "|-" | '""' | "''")
		# A block scalar is fine if a non-empty line follows it.
		if [ -z "$desc" ] || [ -z "$(awk -v e="$end" 'NR > 1 && NR < e && f && /^[ \t]+[^ \t]/ { print; exit } /^description:/ { f = 1 }' "$f")" ]; then
			fail skill.description "$f" "has no description"
		fi
		;;
	esac
	body=$(($(wc -l <"$f") - end))
	[ "$body" -le "$max_lines" ] ||
		fail skill.length "$f" "body is $body lines, limit $max_lines (move detail into references/)"
done

# --- links ------------------------------------------------------------------

# Prints "line<TAB>target" for each inline link outside code.
links() {
	awk '
		/^[ \t]*(```|~~~)/ { fence = !fence; next }
		fence { next }
		{
			s = $0
			gsub(/``[^`]*``/, "", s)
			gsub(/`[^`]*`/, "", s)
			while (match(s, /\]\([^) \t]+( "[^"]*")?\)/)) {
				t = substr(s, RSTART + 2, RLENGTH - 3)
				sub(/ ".*/, "", t)
				print NR "\t" t
				s = substr(s, RSTART + RLENGTH)
			}
		}' "$1"
}

# Prints the anchors of a Markdown file: GitHub's heading slugs (lower case,
# punctuation dropped, spaces to hyphens, -1 -2 for repeats) and HTML ids.
anchors() {
	awk '
		/^[ \t]*(```|~~~)/ { fence = !fence; next }
		fence { next }
		/^#+[ \t]/ {
			h = $0
			sub(/^#+[ \t]+/, "", h)
			sub(/[ \t]+#+[ \t]*$/, "", h)
			# A link in a heading keeps only its text.
			while (match(h, /\[[^\]]*\]\([^)]*\)/)) {
				m = substr(h, RSTART + 1, RLENGTH - 1)
				sub(/\]\(.*/, "", m)
				h = substr(h, 1, RSTART - 1) m substr(h, RSTART + RLENGTH)
			}
			h = tolower(h)
			gsub(/[^a-z0-9 _-]/, "", h)
			gsub(/ /, "-", h)
			n = seen[h]++
			print (n ? h "-" n : h)
		}
		{
			s = $0
			while (match(s, /<a (id|name)="[^"]+"/)) {
				m = substr(s, RSTART, RLENGTH)
				sub(/^<a (id|name)="/, "", m)
				sub(/"$/, "", m)
				print m
				s = substr(s, RSTART + RLENGTH)
			}
		}' "$1"
}

{
	for f in AGENTS.md CLAUDE.md; do [ -f "$f" ] && echo "$f"; done
	[ -d .agents/skills ] && find .agents/skills -name '*.md' \( -type f -o -type l \)
	[ -d tasks ] && find tasks -name '*.md' -type f
} | sort >"$work/files"

while read -r f; do
	[ -e "$f" ] || continue
	links "$f" | while IFS="$(printf '\t')" read -r line target; do
		case "$target" in
		[a-zA-Z]*:*) continue ;; # https:, mailto: ...
		esac
		path="${target%%#*}"
		anchor=""
		case "$target" in *"#"*) anchor="${target#*#}" ;; esac
		if [ -z "$path" ]; then
			resolved="$f"
		else
			case "$path" in
			/*) resolved=".$path" ;;
			*) case "$f" in */*) resolved="${f%/*}/$path" ;; *) resolved="$path" ;; esac ;;
			esac
		fi
		if [ ! -e "$resolved" ]; then
			echo "link.file|$f:$line|$target: no such file"
			continue
		fi
		case "$resolved" in *.md) ;; *) continue ;; esac
		[ -n "$anchor" ] && [ -f "$resolved" ] || continue
		cache="$work/anchors/${resolved//\//_}"
		[ -f "$cache" ] || anchors "$resolved" >"$cache"
		if ! grep -qxF -- "$anchor" "$cache"; then
			echo "link.anchor|$f:$line|$target: no heading with that anchor in $resolved"
		fi
	done
done <"$work/files" >"$work/links"
while IFS='|' read -r id where msg; do
	fail "$id" "$where" "$msg"
done <"$work/links"

# --- tasks ------------------------------------------------------------------

if [ -d tasks ]; then
	set -- tasks/[TE][0-9]*.md
	[ -f "$1" ] || set --
	# One row per task or epic: file, id, status, epic, depends_on.
	: >"$work/tasks"
	[ $# -eq 0 ] || awk '
		function val(s) { sub(/^[a-z_]+:[ \t]*/, "", s); sub(/[ \t]*(#.*)?$/, "", s); return s }
		function row() { print file "\t" id "\t" st "\t" ep "\t" dep }
		FNR == 1 { if (file != "") row(); file = FILENAME; fm = ($0 == "---"); id = st = ep = dep = ""; next }
		fm && $0 == "---" { fm = 0; next }
		fm && /^id:/ { id = val($0) }
		fm && /^status:/ { st = val($0) }
		fm && /^epic:/ { ep = val($0) }
		fm && /^depends_on:/ { dep = val($0); gsub(/[][ ]/, "", dep); gsub(/,/, " ", dep) }
		END { if (file != "") row() }
	' "$@" >"$work/tasks"

	# The rows' own checks, then each epic's checklist ("- [x] [T010](...)"
	# lines) against them.
	set -- tasks/E[0-9]*.md
	[ -f "$1" ] || set --
	awk -F '\t' '
		NR == FNR {
			n++; file[n] = $1; id[n] = $2; st[n] = $3; ep[n] = $4; dep[n] = $5
			byfile[$1] = n; count[$2]++; files[$2] = files[$2] " " $1
			next
		}
		FNR == 1 { e = byfile[FILENAME] }
		# A task planned in an epic but not written yet ("- [ ] T079 ...",
		# no link) may already be depended on.
		/^[ \t]*- \[[ xX]\] T[0-9]+[ \t]/ {
			t = $0
			sub(/^[ \t]*- \[[ xX]\] /, "", t)
			sub(/[ \t].*/, "", t)
			planned[t] = 1
		}
		/^[ \t]*- \[[ xX]\] \[T[0-9]+\]\([^)]*\)/ {
			box = substr($0, index($0, "[") + 1, 1)
			t = substr($0, index($0, "](") + 2)
			sub(/\).*/, "", t)
			k = byfile["tasks/" t]
			# A broken link is link.file`s to report; a task listed in
			# another epic too (moved, shared) is checked only in its own.
			if (!k || ep[k] != id[e]) next
			ticked = (box == "x" || box == "X")
			if (st[k] == "done" && !ticked)
				print "epic.checklist|" FILENAME ":" FNR "|" t " is done but its box is empty"
			else if (ticked && (st[k] == "todo" || st[k] == "in-progress" || st[k] == "blocked"))
				print "epic.checklist|" FILENAME ":" FNR "|" t " is " st[k] " but its box is ticked"
		}
		END {
			for (i = 1; i <= n; i++) {
				base = file[i]; sub(/.*\//, "", base); want = base; sub(/-.*/, "", want)
				if (id[i] != want)
					print "task.id|" file[i] "|id is \"" id[i] "\", want \"" want "\" from the file name"
				else if (count[id[i]] > 1 && !told[id[i]]++)
					print "task.id|tasks|id " id[i] " is used by" files[id[i]]
				if (st[i] !~ /^(todo|in-progress|review|done|blocked)$/)
					print "task.status|" file[i] "|status is \"" st[i] "\", want todo, in-progress, review, done or blocked"
				m = split(dep[i], d, " ")
				for (j = 1; j <= m; j++)
					if (!count[d[j]] && !planned[d[j]])
						print "task.depends|" file[i] "|depends on " d[j] ", which has no task file"
			}
		}
	' "$work/tasks" "$@" </dev/null >"$work/taskerrs"
	while IFS='|' read -r id where msg; do
		fail "$id" "$where" "$msg"
	done <"$work/taskerrs"
fi

if [ "$errors" -gt 0 ]; then
	echo "lint-skills: $errors problem(s)" >&2
	exit 1
fi
