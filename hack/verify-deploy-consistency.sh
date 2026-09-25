#!/usr/bin/env bash
#
# Verifies structural consistency across the three deployment modes
# (deploy/cluster-wide, deploy/single-namespace, deploy/specific-namespaces):
#
#   (a) the pod/container securityContext block(s) and the writable /tmp
#       emptyDir volume+mount are byte-identical across all three
#       deploy/*/deployment.yaml
#   (b) "leases" RBAC is present in each mode's correct file
#   (c) "events" RBAC verbs are granted in every mode
#
# Portable: bash + awk + grep -E + diff only. No GNU-only flags
# (no `find -printf`, no `readlink -f`, no `sed -r`) so this runs
# unmodified on macOS/BSD as well as Linux.
set -eu -o pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "${script_dir}/.." && pwd)
cd "${repo_root}"

fail() {
	echo "verify-deploy-consistency: FAIL: $*" >&2
	exit 1
}

# Extracts every occurrence of a YAML mapping key (and its nested block) from
# a file, matched by an ERE anchored to the key's own line, concatenating all
# occurrences. A block ends when a later non-blank line's indentation is
# less than or equal to the matched key line's indentation.
extract_key_blocks() {
	local file=$1 key_pattern=$2
	awk -v key="${key_pattern}" '
		function indent_of(s,    t) {
			t = s
			sub(/[^ ].*$/, "", t)
			return length(t)
		}
		{
			line = $0
			ind = indent_of(line)
			is_item = (line ~ /^[ ]*-[ ]/) ? 1 : 0
			if (in_block && line !~ /^[ ]*$/) {
				# YAML sequence items conventionally sit at the SAME indentation
				# as their parent key (e.g. "volumes:" / "- name: tmp"), so a
				# same-indent line only ends the block if it is not itself a
				# sequence item continuing that same list.
				continues = (ind > block_indent) || (ind == block_indent && is_item)
				if (!continues) in_block = 0
			}
			if (!in_block && line ~ key) {
				in_block = 1
				block_indent = ind
				print line
				next
			}
			if (in_block) print line
		}
	' "${file}"
}

# Within a block of text, extracts the single YAML sequence item ("- ..." and
# its continuation lines) that contains a line matching item_pattern.
extract_item_containing() {
	local item_pattern=$1
	awk -v pat="${item_pattern}" '
		function indent_of(s,    t) {
			t = s
			sub(/[^ ].*$/, "", t)
			return length(t)
		}
		{
			lines[NR] = $0
			if ($0 ~ /^[ ]*-[ ]/) {
				item_start[NR] = 1
				last_item = NR
			}
			owner[NR] = last_item
		}
		$0 ~ pat && owner[NR] != 0 { hit = owner[NR] }
		END {
			if (hit == 0) exit 0
			hit_indent = indent_of(lines[hit])
			end = NR
			for (i = hit + 1; i <= NR; i++) {
				if (lines[i] ~ /^[ ]*$/) continue
				if (indent_of(lines[i]) <= hit_indent) { end = i - 1; break }
			}
			for (i = hit; i <= end; i++) print lines[i]
		}
	'
}

modes="cluster-wide single-namespace specific-namespaces"

# --- (a) securityContext + /tmp emptyDir volume/mount must be byte-identical ---
sc_prev="" sc_prev_mode="" vol_prev="" vol_prev_mode="" mnt_prev="" mnt_prev_mode=""
for mode in ${modes}; do
	f="deploy/${mode}/deployment.yaml"
	[ -f "${f}" ] || fail "${f} does not exist"

	sc=$(extract_key_blocks "${f}" '^[ ]*securityContext:[ ]*$')
	[ -n "${sc}" ] || fail "no securityContext block found in ${f} (expected: shared pod/container securityContext)"

	volumes_block=$(extract_key_blocks "${f}" '^[ ]*volumes:[ ]*$')
	[ -n "${volumes_block}" ] || fail "no volumes: block found in ${f} (expected: writable /tmp emptyDir volume)"
	vol=$(printf '%s\n' "${volumes_block}" | extract_item_containing 'emptyDir:')
	[ -n "${vol}" ] || fail "no emptyDir volume item found under volumes: in ${f} (expected: writable /tmp emptyDir volume)"

	mounts_block=$(extract_key_blocks "${f}" '^[ ]*volumeMounts:[ ]*$')
	[ -n "${mounts_block}" ] || fail "no volumeMounts: block found in ${f} (expected: /tmp mount)"
	mnt=$(printf '%s\n' "${mounts_block}" | extract_item_containing '^[ ]*mountPath:[ ]*"?/tmp"?[ ]*$')
	[ -n "${mnt}" ] || fail "no volumeMount item for /tmp found under volumeMounts: in ${f} (expected: /tmp mount)"

	if [ -n "${sc_prev}" ] && [ "${sc}" != "${sc_prev}" ]; then
		fail "securityContext block differs between ${sc_prev_mode} and ${mode}:
$(diff <(printf '%s\n' "${sc_prev}") <(printf '%s\n' "${sc}") || true)"
	fi
	if [ -n "${vol_prev}" ] && [ "${vol}" != "${vol_prev}" ]; then
		fail "/tmp emptyDir volume differs between ${vol_prev_mode} and ${mode}:
$(diff <(printf '%s\n' "${vol_prev}") <(printf '%s\n' "${vol}") || true)"
	fi
	if [ -n "${mnt_prev}" ] && [ "${mnt}" != "${mnt_prev}" ]; then
		fail "/tmp volumeMount differs between ${mnt_prev_mode} and ${mode}:
$(diff <(printf '%s\n' "${mnt_prev}") <(printf '%s\n' "${mnt}") || true)"
	fi

	sc_prev=${sc}; sc_prev_mode=${mode}
	vol_prev=${vol}; vol_prev_mode=${mode}
	mnt_prev=${mnt}; mnt_prev_mode=${mode}
done

# --- (b) leases RBAC present in each mode's correct file ---
# Bash-3.2-compatible replacement for an associative array: a case statement
# keyed on mode name (declare -A requires Bash 4+, unavailable in macOS's
# stock /bin/bash).
leases_file_for_mode() {
	case "$1" in
		cluster-wide) printf '%s\n' "deploy/cluster-wide/clusterrole.yaml" ;;
		single-namespace) printf '%s\n' "deploy/single-namespace/role.yaml" ;;
		specific-namespaces) printf '%s\n' "deploy/specific-namespaces/role-leaderelection.yaml" ;;
	esac
}
for mode in ${modes}; do
	f=$(leases_file_for_mode "${mode}")
	[ -f "${f}" ] || fail "${f} does not exist (expected: leases RBAC for ${mode})"
	grep -qE '^[ ]*-[ ]*leases[ ]*$' "${f}" || fail "no 'leases' resource found in ${f}"
done

# --- (c) events RBAC verbs granted in every mode ---
events_file_for_mode() {
	case "$1" in
		cluster-wide) printf '%s\n' "deploy/cluster-wide/clusterrole.yaml" ;;
		single-namespace) printf '%s\n' "deploy/single-namespace/role.yaml" ;;
		specific-namespaces) printf '%s\n' "deploy/specific-namespaces/role.yaml" ;;
	esac
}
for mode in ${modes}; do
	f=$(events_file_for_mode "${mode}")
	[ -f "${f}" ] || fail "${f} does not exist"
	grep -qE '^[ ]*-[ ]*events[ ]*$' "${f}" || fail "no 'events' resource found in ${f}"
done

echo "verify-deploy-consistency: OK"
