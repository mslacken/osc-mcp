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

def run_command(cmd: List[str]) -> Optional[str]:
    """Runs a shell command and returns stdout if successful, else None."""
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        if "failed" in e.stdout:
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

def main():
    parser = argparse.ArgumentParser(description="Collect data from OBS requests.")
    parser.add_argument("n0", type=int, help="Lower bound of request IDs")
    parser.add_argument("n1", type=int, help="Upper bound of request IDs")
    parser.add_argument("--rate-limit", type=float, default=1.0, help="Seconds between requests")
    parser.add_argument("--bypass", action="store_true", help="Only generate random numbers and exit")
    parser.add_argument("--sigma", type=float, help="Sigma for Gaussian distribution (default: (n1-n0)/2)")
    parser.add_argument("--output", default="data/changes.json", help="Output JSON file")
    parser.add_argument("--count", type=int, default=10, help="Number of requests to try")

    args = parser.parse_args()

    if args.bypass:
        print(f"Generating {args.count} random numbers in range [{args.n0}, {args.n1}] (max at {args.n1}):")
        for _ in range(args.count):
            print(get_truncated_gauss(args.n0, args.n1, args.sigma))
        return

    # Check for binaries
    req_getter = "bin/request_getter"
    changelog_ext = "bin/changelog_extractor"

    missing = []
    if not os.path.exists(req_getter): missing.append(req_getter)
    if not os.path.exists(changelog_ext): missing.append(changelog_ext)

    if missing:
        print(f"Error: Missing binaries: {', '.join(missing)}")
        print("Please run 'make tools' to build them.")
        sys.exit(1)

    os.makedirs(os.path.dirname(args.output), exist_ok=True)

    success_count = 0
    attempts = 0

    while success_count < args.count:
        req_id = get_truncated_gauss(args.n0, args.n1, args.sigma)
        attempts += 1
        print(f"Attempt {attempts}: Checking request {req_id}...", end=" ", flush=True)

        # 1. Check if request is accepted to Factory and get source details
        # -F returns "source.project package revision"
        fields = "source.project,package,revision"
        cmd = [req_getter, str(req_id), "-f", "state=accepted", "-f", "target=openSUSE:Factory", "-F", fields]
        
        output = run_command(cmd)
        
        if not output or output == "failed":
            print("Skipped (not accepted to Factory or failed).")
        else:
            # Output format: "proj1,proj2 pkg1,pkg2 rev1,rev2"
            try:
                parts = output.split()
                if len(parts) >= 3:
                    # Take first element if multiple actions exist
                    project = parts[0].split(',')[0]
                    package = parts[1].split(',')[0]
                    revision = parts[2].split(',')[0]

                    print(f"Found! Fetching changelog for {project}/{package}@{revision}...", end=" ", flush=True)
                    
                    # 2. Extract changelog data
                    cmd_ext = [changelog_ext, project, package, revision]
                    ext_output = run_command(cmd_ext)
                    
                    if ext_output:
                        try:
                            data = json.loads(ext_output)
                            data["request_id"] = req_id
                            append_to_json_list(args.output, data)
                            print("Done.")
                            success_count += 1
                        except json.JSONDecodeError:
                            print("Error parsing changelog extractor output.")
                    else:
                        print("Failed to extract changelog.")
                else:
                    print(f"Unexpected output format from request_getter: {output}")
            except Exception as e:
                print(f"Error processing output: {e}")

        if success_count < args.count:
            time.sleep(args.rate_limit)

    print(f"\nSuccessfully collected {success_count} entries to {args.output}")

if __name__ == "__main__":
    main()
