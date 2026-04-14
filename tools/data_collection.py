#!/usr/bin/env python3
import argparse
import json
import os
import random
import subprocess
import sys
import time
from typing import List, Optional

def get_truncated_gauss(n0: int, n1: int, sigma: float = None) -> int:
    """
    Returns a random integer between n0 and n1 following a truncated Gaussian
    distribution with its maximum (mean) at n1.
    """
    if sigma is None:
        sigma = (n1 - n0) / 2.0
    
    while True:
        # mu = n1 to have maximum at n1
        val = random.gauss(n1, sigma)
        if n0 <= val <= n1:
            return int(round(val))

def run_command(cmd: List[str], timeout: Optional[float] = None) -> Optional[str]:
    """Runs a shell command and returns stdout if successful, else None."""
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True, timeout=timeout)
        return result.stdout.strip()
    except subprocess.TimeoutExpired:
        raise
    except subprocess.CalledProcessError as e:
        if e.stdout and "failed" in e.stdout:
            return None
        # Other errors might be real issues
        return None
    except Exception:
        return None

def append_to_json_list(file_path: str, data: dict):
    """
    Appends a dictionary to a JSON list in a file.
    Ensures the file is valid JSON even if interrupted.
    """
    existing_data = []
    if os.path.exists(file_path):
        try:
            with open(file_path, 'r') as f:
                existing_data = json.load(f)
                if not isinstance(existing_data, list):
                    existing_data = []
        except (json.JSONDecodeError, IOError):
            existing_data = []

    existing_data.append(data)

    # Atomic-ish write: write to temp and rename
    temp_path = file_path + ".tmp"
    with open(temp_path, 'w') as f:
        json.dump(existing_data, f, indent=2)
    os.replace(temp_path, file_path)

def clean_obs_diff(diff_text: str) -> str:
    """Removes the OBS limiter line, author/date line, diff hunk headers, and empty lines from diffs."""
    if not diff_text:
        return ""
    
    marker = "-------------------------------------------------------------------"
    lines = diff_text.splitlines()
    new_lines = []
    skip_next = False
    
    for line in lines:
        if marker in line:
            skip_next = True
            continue
        if skip_next:
            skip_next = False
            continue
        if line.startswith("@@") or not line.strip():
            continue
        new_lines.append(line)
    
    return "\n".join(new_lines).strip()

def process_request(req_id: int, req_getter: str, changelog_ext: str, output_file: str, timeout: Optional[float] = None) -> bool:
    """Processes a single request ID. Returns True if successful and data was appended."""
    # 1. Check if request is accepted to Factory and get source details
    cmd = [req_getter, str(req_id), "-f", "state=accepted", "-f", "target=openSUSE:Factory"]
    
    try:
        output = run_command(cmd, timeout=timeout)
    except subprocess.TimeoutExpired:
        print("Skipped (timeout reached while checking request).")
        return False
    
    if not output:
        print("Skipped (no output from request_getter).")
        return False

    try:
        data = json.loads(output)
        if "error" in data:
            print("Skipped (not accepted to Factory or failed).")
            return False

        # Extract fields from the first action
        actions = data.get("Actions", []) or data.get("action", [])
        if not actions:
            print("Skipped (no actions found in request).")
            return False
        
        action = actions[0]
        project = action.get("Source", {}).get("Project") or action.get("source", {}).get("project")
        package = action.get("Source", {}).get("Package") or action.get("source", {}).get("package")
        revision = action.get("Source", {}).get("Rev") or action.get("source", {}).get("rev")

        if project and package and revision:
            # Extract more from sourcediff
            changed_files = []
            removed_files = []
            added_files = []
            spec_diff = ""
            changes_diff = ""

            sourcediff = action.get("SourceDiff", {}) or action.get("sourcediff", {})
            if sourcediff:
                diff_files = sourcediff.get("Files", []) or sourcediff.get("files", [])
                for df in diff_files:
                    new_file = df.get("New", {}) or df.get("new", {})
                    old_file = df.get("Old", {}) or df.get("old", {})
                    name = new_file.get("Name") or new_file.get("name")
                    old_name = old_file.get("Name") or old_file.get("name")
                    diff_data = df.get("Diff", {}).get("Data") or df.get("diff", {}).get("data", "")

                    if name:
                        top_level = name.split('/')[0]
                        if not old_name and top_level not in added_files:
                            added_files.append(top_level)
                        if top_level not in changed_files and top_level not in added_files:
                            changed_files.append(top_level)
                        
                        if '/' in name:
                            continue
                        
                        if name.endswith(".spec"):
                            spec_diff = diff_data
                        elif name.endswith(".changes"):
                            changes_diff = diff_data
                    elif old_name:
                        top_level = old_name.split('/')[0]
                        if top_level not in removed_files:
                            removed_files.append(top_level)

            print(f"Found! Fetching changelog for {project}/{package}@{revision}...", end=" ", flush=True)

            # 2. Extract changelog data
            fields_ext = "version,source,archive_changelog,url,github_release_notes"
            cmd_ext = [changelog_ext, project, package, revision, "-F", fields_ext]
            
            try:
                ext_output = run_command(cmd_ext, timeout=timeout)
            except subprocess.TimeoutExpired:
                print("Failed (timeout reached while extracting changelog).")
                return False

            if ext_output:
                try:
                    data_ext = json.loads(ext_output)
                    
                    archive_cl = data_ext.get("archive_changelog")
                    github_rn = data_ext.get("github_release_notes")
                    
                    if not archive_cl and not github_rn:
                        print("Skipped (empty changelog and release notes).")
                        return False

                    # Combine data
                    final_data = {
                        "request_id": req_id,
                        "project": project,
                        "package": package,
                        "version": data_ext.get("version"),
                        "changed_files": changed_files,
                        "removed_files": removed_files,
                        "added_files": added_files,
                        "spec_diff": clean_obs_diff(spec_diff),
                        "changes_diff": clean_obs_diff(changes_diff),
                        "source_file": data_ext.get("source"),
                        "archive_changelog": archive_cl,
                        "url": data_ext.get("url"),
                        "github_release_notes": github_rn
                    }
                    append_to_json_list(output_file, final_data)
                    print("Done.")
                    return True

                except json.JSONDecodeError:
                    print("Error parsing changelog extractor output.")
            else:
                print("Failed to extract changelog.")
        else:
            print(f"Missing required fields in request_getter output for {req_id}")
    except json.JSONDecodeError:
        print(f"Unexpected non-JSON output from request_getter for {req_id}: {output}")
    except Exception as e:
        import traceback
        print(f"Error processing output for {req_id}: {e}")
    return False

def load_existing_ids(output_file: str, failed_file: str, extra_sources: List[str] = None):
    """Loads request IDs that are already processed or marked as failed."""
    success_ids = set()
    failed_ids = set()

    sources = [output_file]
    if extra_sources:
        sources.extend(extra_sources)

    for source in set(sources):
        if not source or not os.path.exists(source):
            continue
        try:
            with open(source, "r") as f:
                data = json.load(f)
                if isinstance(data, list):
                    for item in data:
                        if isinstance(item, dict) and "request_id" in item:
                            success_ids.add(item["request_id"])
        except (json.JSONDecodeError, IOError):
            pass

    if os.path.exists(failed_file):
        try:
            with open(failed_file, "r") as f:
                data = json.load(f)
                if isinstance(data, list):
                    failed_ids = set(data)
        except (json.JSONDecodeError, IOError):
            pass

    return success_ids, failed_ids

def save_failed_ids(failed_file: str, failed_ids: set):
    """Saves the set of failed IDs to a JSON file."""
    temp_path = failed_file + ".tmp"
    with open(temp_path, 'w') as f:
        json.dump(sorted(list(failed_ids)), f, indent=2)
    os.replace(temp_path, failed_file)

def main():
    parser = argparse.ArgumentParser(description="Collect data from OBS requests.")
    parser.add_argument("n0", type=int, nargs='?', help="Lower bound of request IDs (optional if --request is used)")
    parser.add_argument("n1", type=int, nargs='?', help="Upper bound of request IDs (optional if --request is used)")
    parser.add_argument("--request", "-r", type=int, help="Process just this single request ID")
    parser.add_argument("--rate-limit", type=float, default=1.0, help="Seconds between requests")
    parser.add_argument("--bypass", action="store_true", help="Only generate random numbers and exit")
    parser.add_argument("--sigma", type=float, help="Sigma for Gaussian distribution (default: (n1-n0)/2)")
    parser.add_argument("--output", default="changes.json", help="Output JSON file")
    parser.add_argument("--failed-file", default="failed.json", help="File to store failed request IDs")
    parser.add_argument("--count", type=int, default=10, help="Number of requests to try")
    parser.add_argument("--timeout", type=float, default=30.0, help="Timeout for commands in seconds")

    args = parser.parse_args()

    if args.bypass:
        if args.n0 is None or args.n1 is None:
            print("Error: n0 and n1 are required for --bypass")
            sys.exit(1)
        print(f"Generating {args.count} random numbers in range [{args.n0}, {args.n1}] (max at {args.n1}):")
        for _ in range(args.count):
            print(get_truncated_gauss(args.n0, args.n1, args.sigma))
        return

    # Check for binaries
    req_getter = "bin/request_getter"
    changelog_ext = "bin/changelog_extractor"

    missing = []
    if not os.path.exists(req_getter):
        missing.append(req_getter)
    if not os.path.exists(changelog_ext):
        missing.append(changelog_ext)

    if missing:
        print(f"Error: Missing binaries: {', '.join(missing)}")
        print("Please run 'make tools' to build them.")
        sys.exit(1)

    output_dir = os.path.dirname(args.output)
    if output_dir:
        os.makedirs(output_dir, exist_ok=True)

    success_ids, failed_ids = load_existing_ids(args.output, args.failed_file, ["changes.json"])
    print(f"Loaded {len(success_ids)} successful and {len(failed_ids)} failed IDs.")

    if args.request:
        if args.request in success_ids:
            print(f"Request {args.request} already processed. Skipping.")
            return
        
        print(f"Checking single request {args.request}...", end=" ", flush=True)
        if process_request(args.request, req_getter, changelog_ext, args.output, timeout=args.timeout):
            print("Success.")
        else:
            failed_ids.add(args.request)
            save_failed_ids(args.failed_file, failed_ids)
            print("Failed.")
        return

    if args.n0 is None or args.n1 is None:
        print("Error: Either provide n0 and n1 for random collection, or use --request for a single ID.")
        sys.exit(1)

    success_count = 0
    unsuccessful_count = 0
    attempts = 0

    while success_count < args.count:
        req_id = get_truncated_gauss(args.n0, args.n1, args.sigma)
        
        if req_id in success_ids or req_id in failed_ids:
            continue

        attempts += 1
        print(f"Attempt: {attempts} Count: {success_count} Checking request: {req_id}...", end=" ", flush=True)

        if process_request(req_id, req_getter, changelog_ext, args.output, timeout=args.timeout):
            success_count += 1
            success_ids.add(req_id)
        else:
            unsuccessful_count += 1
            failed_ids.add(req_id)
            save_failed_ids(args.failed_file, failed_ids)

        if success_count < args.count:
            time.sleep(args.rate_limit)

    print(f"\nSuccessfully collected {success_count} entries to {args.output}")
    print(f"Unsuccessful attempts in this session: {unsuccessful_count}")

if __name__ == "__main__":
    main()
