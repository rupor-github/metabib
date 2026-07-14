#!/bin/bash

# Synology task scheduler has a problem running scripts under non-root user.

if [[ -z "${1:-}" || -z "${2:-}" || -z "${3:-}" ]]; then
	echo "Usage: $0 <library-root> <full|reindex> <mhl|flib|both> [run-as-user]"
	exit 1
fi

case "$2" in
	full)
		mode="full"
		;;
	reindex|re-index|index)
		mode="reindex"
		;;
	*)
		echo "Unknown mode: $2"
		echo "Usage: $0 <library-root> <full|reindex> <mhl|flib|both> [run-as-user]"
		exit 1
		;;
esac

case "$3" in
	mhl|flib|both)
		inpx_mode="$3"
		;;
	*)
		echo "Unknown INPX mode: $3"
		echo "Usage: $0 <library-root> <full|reindex> <mhl|flib|both> [run-as-user]"
		exit 1
		;;
esac

run_as_user="${4:-}"

if [[ -n "${run_as_user}" ]]; then
	user_dir=$(eval echo "~${run_as_user}")
	if [[ -n "${user_dir}" ]]; then
		cd "${user_dir}" || exit 1
	fi
fi

# -----------------------------------------------------------------------------
# Following variables could be changed
# -----------------------------------------------------------------------------

# Fetch profile name from metabib.yaml and directory name under <library-root>.
name="flibusta"

# Number of attempts for downloading each index page and file.
retries=10

# Per-request timeout in seconds. File downloads must receive each chunk within
# this interval.
timeout=300

# Download chunk size in decimal megabytes.
chunksize=1

# Set to true, or run with METABIB_VERBOSE=1, to enable detailed progress logs.
verbose=false

# -----------------------------------------------------------------------------
# Main body
# -----------------------------------------------------------------------------

# Timestamp used to keep downloaded SQL dumps and logs unique per run.
cdate="$(date +%Y%m%d_%H%M%S)"

# Directory containing this script and the metabib executable.
mydir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Root directory for library archives, update archives, SQL dumps, and INPX.
root="$1"

# Finalized FB2 archive directory.
adir="${root}/${name}"

# Directory for generated INPX and intermediate merged JSONL files.
odir="${root}/inpx"

# Per-run SQL dump directory populated by `metabib fetch`.
wdir="${adir}_${cdate}"

# Daily update archive directory populated by `metabib fetch`.
udir="${root}/upd_${name}"

# Script-level log capturing console output from all commands. This script expects
# metabib.yaml to route metabib diagnostics through console logging only:
#
#   logging:
#     console:
#       level: debug
#     file:
#       level: none
#
# With that configuration, this single per-run log contains script messages,
# metabib debug messages, and MariaDB process/client output. Enabling metabib
# file logging is still supported by the application, but this script no longer
# manages or renames metabib.log.
glog="${mydir}/${name}_${mode}_${inpx_mode}_${cdate}.log"

# metabib executable. It is expected to be next to this script.
metabib="${mydir}/metabib"

metabib_args=()
if [[ "${verbose}" == "true" || "${METABIB_VERBOSE:-0}" == "1" ]]; then
	metabib_args+=(--verbose)
fi
if [[ -f "${mydir}/metabib.yaml" ]]; then
	metabib_args+=(--config "${mydir}/metabib.yaml")
fi

detect_dump_date() {
	local dump_dir="$1"
	local sql line

	for sql in "${dump_dir}"/*.sql; do
		[[ -e "${sql}" ]] || continue
		while IFS= read -r line; do
			if [[ "${line}" =~ --[[:space:]]*Dump[[:space:]]completed[[:space:]]on[[:space:]]([0-9]{4})-([0-9]{2})-([0-9]{2}) ]]; then
				printf '%s%s%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
				return 0
			fi
		done <"${sql}"
	done

	return 1
}

latest_dump_dir() {
	local dirs
	shopt -s nullglob
	dirs=("${adir}"_*)
	shopt -u nullglob

	if (( ${#dirs[@]} == 0 )); then
		return 1
	fi
	printf '%s\n' "${dirs[@]}" | sort -nr | head -n 1
}

log_phase() {
	echo
	echo "========================================================================"
	echo "$(date '+%Y-%m-%d %H:%M:%S')  $*"
	echo "========================================================================"
}

build_mhl_inpx() {
	local merge_prefix="$1" res

	log_phase "Building MyHomeLib INPX"

	"${metabib}" "${metabib_args[@]}" mhl-inpx \
		--input "${merge_prefix}" \
		--output "${odir}/${name}_mhl"

	res=$?
	if (( res != 0 )); then
		echo "Unable to build MyHomeLib INPX - ${res}"
		exit 1
	fi
}

build_flib_inpx() {
	local merge_prefix="$1" res

	log_phase "Building FLibrary INPX"

	"${metabib}" "${metabib_args[@]}" flib-inpx \
		--input "${merge_prefix}" \
		--output "${odir}/${name}_flib" \
		--source-lib "${name}"

	res=$?
	if (( res != 0 )); then
		echo "Unable to build FLibrary INPX - ${res}"
		exit 1
	fi
}

build_selected_inpx() {
	local merge_prefix="$1"

	case "${inpx_mode}" in
		mhl)
			build_mhl_inpx "${merge_prefix}"
			;;
		flib)
			build_flib_inpx "${merge_prefix}"
			;;
		both)
			build_mhl_inpx "${merge_prefix}"
			build_flib_inpx "${merge_prefix}"
			;;
	esac
}

build_inpx_from_existing_data() {
	local dump_dir="$1"
	local dump_date merge_prefix res

	if ! dump_date=$(detect_dump_date "${dump_dir}"); then
		echo "Unable to detect SQL dump date in ${dump_dir}"
		exit 1
	fi

	merge_prefix="${odir}/${name}_${dump_date}"

	log_phase "Building ${name} cache manifests"

	"${metabib}" "${metabib_args[@]}" cache \
		--database-dumps "${dump_dir}" \
		--archives "${adir}"

	res=$?
	if (( res != 0 )); then
		echo "Unable to build cache manifests - ${res}"
		exit 1
	fi

	log_phase "Merging ${name} dataset"

	"${metabib}" "${metabib_args[@]}" merge \
		--database-dumps "${dump_dir}" \
		--archives "${adir}" \
		--output "${merge_prefix}"

	res=$?
	if (( res != 0 )); then
		echo "Unable to merge dataset - ${res}"
		exit 1
	fi

	build_selected_inpx "${merge_prefix}"
}

exec 3>&1 4>&2
trap 'exec 2>&4 1>&3' 0 1 2 3 RETURN
exec 1>"${glog}" 2>&1

if [[ "${mode}" == "reindex" ]]; then
	log_phase "Selecting latest ${name} SQL dump directory"
	if ! wdir=$(latest_dump_dir); then
		echo "Unable to find existing SQL dump directory matching ${adir}_*"
		exit 1
	fi
	build_inpx_from_existing_data "${wdir}"
	exit 0
fi

log_phase "Downloading ${name}"

"${metabib}" "${metabib_args[@]}" fetch \
	--library "${name}" \
	--retry "${retries}" \
	--timeout "${timeout}" \
	--chunksize "${chunksize}" \
	--continue \
	--to "${udir}" \
	--tosql "${wdir}"

res=$?
if (( res == 1 )); then
	echo "metabib fetch error!"
	exit 1
elif (( res == 0 )); then
	echo "No archive updates..."
	exit 0
fi

log_phase "Cleaning old SQL dump directories"

# Clean old database directories - we have at least one good download.
find "${root}" -maxdepth 1 -type d -name "${name}_*" | sort -nr | tail -n +6 | xargs -r -I {} rm -rf {}/

log_phase "Rolling up ${name} archives"

"${metabib}" "${metabib_args[@]}" rollup \
	--archives "${adir}" \
	--updates "${udir}"

res=$?
if (( res == 1 )); then
	echo "metabib rollup error!"
	exit 1
fi

log_phase "Cleaning old update archives"

# Clean updates leaving last ones so fetch does not download unnecessary updates next time.
find "${udir}" -type f | sort -nr | tail -n +11 | xargs -r -I {} rm -r {}

if (( res == 0 )); then
	echo "Nothing to do..."
	exit 0
fi

build_inpx_from_existing_data "${wdir}"
