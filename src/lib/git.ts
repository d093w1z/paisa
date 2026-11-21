import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

dayjs.extend(relativeTime);

export function shortHash(hash: string) {
  return hash.slice(0, 7);
}

export function relativeCommitTime(when: string) {
  return dayjs(when).fromNow();
}

// Validates a remote HTTPS clone URL entered manually for the "other
// provider" flow. SSH-style remotes (git@host:... or ssh://...) are
// rejected up front, since pushing over SSH isn't supported.
export function isValidHttpsRemoteURL(url: string) {
  if (!url) {
    return false;
  }

  if (url.startsWith("git@") || url.startsWith("ssh://")) {
    return false;
  }

  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" || parsed.protocol === "http:";
  } catch {
    return false;
  }
}

// RFC 8628 device-flow polling: the server may ask us to slow down, in
// which case we should poll less often than the originally advertised
// interval. Returns the interval (in seconds) to wait before the next poll.
export function nextPollIntervalSeconds(currentIntervalSeconds: number, status: string) {
  if (status === "slow_down") {
    return currentIntervalSeconds + 5;
  }
  return currentIntervalSeconds;
}

export function filterRepos<T extends { name: string; full_name: string }>(
  repos: T[],
  query: string
) {
  const trimmed = query.trim().toLowerCase();
  if (!trimmed) {
    return repos;
  }
  return repos.filter(
    (repo) =>
      repo.name.toLowerCase().includes(trimmed) || repo.full_name.toLowerCase().includes(trimmed)
  );
}
