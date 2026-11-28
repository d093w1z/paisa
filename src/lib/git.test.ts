import { describe, expect, test } from "bun:test";
import { filterRepos, isValidHttpsRemoteURL, nextPollIntervalSeconds, shortHash } from "./git";

describe("git", () => {
  test("shortHash", () => {
    expect(shortHash("d1973f516f5eb724614173e03a29df66fb72ea4c")).toBe("d1973f5");
    expect(shortHash("abc")).toBe("abc");
  });

  test("isValidHttpsRemoteURL", () => {
    expect(isValidHttpsRemoteURL("https://github.com/user/repo.git")).toBe(true);
    expect(isValidHttpsRemoteURL("http://gitlab.example.com/user/repo.git")).toBe(true);
    expect(isValidHttpsRemoteURL("git@github.com:user/repo.git")).toBe(false);
    expect(isValidHttpsRemoteURL("ssh://git@github.com/user/repo.git")).toBe(false);
    expect(isValidHttpsRemoteURL("not a url")).toBe(false);
    expect(isValidHttpsRemoteURL("")).toBe(false);
  });

  test("nextPollIntervalSeconds", () => {
    expect(nextPollIntervalSeconds(5, "pending")).toBe(5);
    expect(nextPollIntervalSeconds(5, "slow_down")).toBe(10);
  });

  test("filterRepos", () => {
    const repos = [
      { name: "finance", full_name: "user/finance" },
      { name: "notes", full_name: "user/notes" }
    ];
    expect(filterRepos(repos, "")).toEqual(repos);
    expect(filterRepos(repos, "fin")).toEqual([repos[0]]);
    expect(filterRepos(repos, "USER/NOTES")).toEqual([repos[1]]);
    expect(filterRepos(repos, "zzz")).toEqual([]);
  });
});
