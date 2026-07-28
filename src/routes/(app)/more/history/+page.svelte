<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import _ from "lodash";
  import * as toast from "bulma-toast";
  import {
    ajax,
    buildDirectoryTree,
    type GitCommitInfo,
    type GitRemoteRepo,
    type GitStatus,
    type LedgerFile
  } from "$lib/utils";
  import { filterRepos, isValidHttpsRemoteURL, relativeCommitTime, shortHash } from "$lib/git";
  import FileTree from "$lib/components/FileTree.svelte";
  import DiffViewModal from "$lib/components/DiffViewModal.svelte";
  import Modal from "$lib/components/Modal.svelte";

  let status: GitStatus = null;
  let files: LedgerFile[] = [];
  let selectedFile: LedgerFile = null;
  let commits: GitCommitInfo[] = [];
  let loadingCommits = false;

  let diffOpen = false;
  let diffOldFiles: LedgerFile[] = [];
  let diffNewFiles: LedgerFile[] = [];

  let connecting = false;
  let deviceModalOpen = false;
  let deviceUserCode = "";
  let deviceVerificationURI = "";
  let pollTimer: ReturnType<typeof setTimeout> = null;
  let pollSessionId: string = null;

  let pickerOpen = false;
  let pickerSessionId: string = null;
  let pickerRepos: GitRemoteRepo[] = [];
  let pickerQuery = "";
  let pickerBranch = "main";
  let pickerLoading = false;

  let tokenProvider: "github" | "gitlab" = "github";
  let tokenValue = "";
  let tokenSubmitting = false;

  let otherUrl = "";
  let otherBranch = "main";
  let otherToken = "";
  let otherSubmitting = false;

  let pushing = false;

  let initializing = false;
  let changingRepoSetup = false;
  let connectionOpen = false;
  let connectionOpenInitialized = false;

  $: filteredPickerRepos = filterRepos(pickerRepos, pickerQuery);

  onMount(async () => {
    await Promise.all([loadStatus(), loadFiles()]);
  });

  onDestroy(() => {
    if (pollTimer) {
      clearTimeout(pollTimer);
    }
  });

  async function loadStatus() {
    status = await ajax("/api/git/status");

    if (!connectionOpenInitialized) {
      connectionOpenInitialized = true;
      connectionOpen = !!status.remote_url;
    }
  }

  async function loadFiles() {
    const { files: f } = await ajax("/api/editor/files");
    files = f;
  }

  async function selectFile(file: LedgerFile) {
    selectedFile = file;
    commits = [];
    loadingCommits = true;
    try {
      ({ commits } = await ajax(`/api/git/history?file=${encodeURIComponent(file.name)}`));
    } finally {
      loadingCommits = false;
    }
  }

  async function viewDiff(commit: GitCommitInfo) {
    const [{ file: current }, old] = await Promise.all([
      ajax("/api/editor/file", {
        method: "POST",
        body: JSON.stringify({ name: selectedFile.name }),
        background: true
      }),
      ajax(`/api/git/content?file=${encodeURIComponent(selectedFile.name)}&commit=${commit.hash}`)
    ]);

    if (old.error) {
      toast.toast({ message: old.error, type: "is-danger" });
      return;
    }

    diffOldFiles = [{ type: "file", name: selectedFile.name, content: old.content, versions: [] }];
    diffNewFiles = [{ type: "file", name: current.name, content: current.content, versions: [] }];
    diffOpen = true;
  }

  async function startOAuth(provider: "github" | "gitlab") {
    connecting = true;
    try {
      const resp = await ajax("/api/git/oauth/start", {
        method: "POST",
        body: JSON.stringify({ provider }),
        background: true
      });

      if (resp.error || !resp.session_id) {
        toast.toast({ message: resp.error || "Failed to start connection", type: "is-danger" });
        return;
      }

      deviceUserCode = resp.user_code;
      deviceVerificationURI = resp.verification_uri;
      pollSessionId = resp.session_id;
      deviceModalOpen = true;
      schedulePoll(resp.interval || 5);
    } finally {
      connecting = false;
    }
  }

  function schedulePoll(intervalSeconds: number) {
    pollTimer = setTimeout(async () => {
      const sessionId = pollSessionId;
      const { status: pollStatus } = await ajax(`/api/git/oauth/poll?session_id=${sessionId}`);

      if (pollStatus === "success") {
        deviceModalOpen = false;
        await openPicker(sessionId);
      } else if (pollStatus === "pending") {
        schedulePoll(intervalSeconds);
      } else if (pollStatus === "slow_down") {
        schedulePoll(intervalSeconds + 5);
      } else {
        deviceModalOpen = false;
        toast.toast({ message: `Connection ${pollStatus}`, type: "is-danger" });
      }
    }, intervalSeconds * 1000);
  }

  function cancelDeviceFlow() {
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
    deviceModalOpen = false;
  }

  async function submitManualToken() {
    tokenSubmitting = true;
    try {
      const resp = await ajax("/api/git/token/session", {
        method: "POST",
        body: JSON.stringify({ provider: tokenProvider, token: tokenValue }),
        background: true
      });
      tokenValue = "";

      if (resp.error || !resp.session_id) {
        toast.toast({ message: resp.error || "Failed to validate token", type: "is-danger" });
        return;
      }

      await openPicker(resp.session_id);
    } finally {
      tokenSubmitting = false;
    }
  }

  async function openPicker(sessionId: string) {
    pickerSessionId = sessionId;
    pickerQuery = "";
    pickerBranch = "main";
    pickerLoading = true;
    pickerOpen = true;
    try {
      const { repos, error } = await ajax(`/api/git/repos?session_id=${sessionId}`);
      if (error) {
        toast.toast({ message: error, type: "is-danger" });
        pickerOpen = false;
        return;
      }
      pickerRepos = repos || [];
    } finally {
      pickerLoading = false;
    }
  }

  async function selectRepo(repo: GitRemoteRepo) {
    const resp = await ajax("/api/git/repos/select", {
      method: "POST",
      body: JSON.stringify({
        session_id: pickerSessionId,
        clone_url: repo.clone_url,
        branch: pickerBranch
      }),
      background: true
    });

    if (!resp.success) {
      toast.toast({ message: resp.message || "Failed to connect repository", type: "is-danger" });
      return;
    }

    pickerOpen = false;
    toast.toast({ message: `Connected to ${repo.full_name}`, type: "is-success" });
    await loadStatus();
  }

  async function submitOtherProvider() {
    if (!isValidHttpsRemoteURL(otherUrl)) {
      toast.toast({
        message: "Please enter a valid HTTPS remote URL (SSH remotes aren't supported)",
        type: "is-danger"
      });
      return;
    }

    otherSubmitting = true;
    try {
      const resp = await ajax("/api/git/token", {
        method: "POST",
        body: JSON.stringify({ url: otherUrl, branch: otherBranch, token: otherToken }),
        background: true
      });
      otherToken = "";

      if (!resp.success) {
        toast.toast({ message: resp.message || "Failed to connect", type: "is-danger" });
        return;
      }

      toast.toast({ message: "Connected", type: "is-success" });
      await loadStatus();
    } finally {
      otherSubmitting = false;
    }
  }

  async function setupRepo(mode: "existing" | "new") {
    if (
      mode === "new" &&
      status?.parent_repo_available &&
      !confirm(
        `A parent repository was found at ${status.parent_repo_root}. Creating a new repository will instead track only the ledger folder, separately from that parent repository. Continue?`
      )
    ) {
      return;
    }

    initializing = true;
    try {
      const resp = await ajax("/api/git/init", {
        method: "POST",
        body: JSON.stringify({ mode }),
        background: true
      });

      if (!resp.success) {
        toast.toast({ message: resp.message || "Failed to set up repository", type: "is-danger" });
        return;
      }

      changingRepoSetup = false;
      toast.toast({ message: "Repository ready", type: "is-success" });
      await loadStatus();
    } finally {
      initializing = false;
    }
  }

  async function push() {
    pushing = true;
    try {
      const resp = await ajax("/api/git/push", { method: "POST", background: true });
      if (!resp.success) {
        toast.toast({ message: resp.message || "Push failed", type: "is-danger" });
      } else {
        toast.toast({ message: "Pushed", type: "is-success" });
      }
      await loadStatus();
    } finally {
      pushing = false;
    }
  }

  async function disconnect() {
    if (!confirm("Disconnect the remote? Local commit history is kept.")) {
      return;
    }

    const resp = await ajax("/api/git/disconnect", { method: "POST", background: true });
    if (!resp.success) {
      toast.toast({ message: resp.message || "Failed to disconnect", type: "is-danger" });
      return;
    }

    await loadStatus();
  }
</script>

<div class="section">
  <div class="container is-fluid">
    {#if !status}
      <!-- loading -->
    {:else if !status.enabled}
      <div class="box px-3" style="max-width: 800px;">
        <p>
          Git based version history is disabled. Enable it on the
          <a href="/more/config">Configuration</a> page.
        </p>
      </div>
    {:else}
      <div class="columns">
        <div class="column is-12">
          <div class="box px-3" style="max-width: 1024px;">
            <h4 class="title is-5">Local repository</h4>
            <p class="mb-3">
              Version history works entirely locally using git — no account or remote is required.
              Connecting a remote (below) is optional, for backing up or syncing history elsewhere.
            </p>

            {#if !status.repo_detected}
              {#if status.parent_repo_available}
                <article class="message is-info">
                  <div class="message-body">
                    A git repository was found at <code>{status.parent_repo_root}</code>, above the
                    ledger folder. Choose whether to track history there, or keep a repository
                    dedicated just to the ledger files.
                  </div>
                </article>
                <div class="field is-grouped">
                  <div class="control">
                    <button
                      class="button is-success {initializing && 'is-loading'}"
                      on:click={() => setupRepo("existing")}>Use the existing repository</button
                    >
                  </div>
                  <div class="control">
                    <button
                      class="button {initializing && 'is-loading'}"
                      on:click={() => setupRepo("new")}
                      >Create a new repository for just the ledgers</button
                    >
                  </div>
                </div>
              {:else}
                <article class="message is-warning">
                  <div class="message-body">
                    No git repository found for the ledger folder yet{status.auto_init
                      ? " — one will be created automatically on the next save or sync"
                      : ""}.
                  </div>
                </article>
                <div class="control">
                  <button
                    class="button is-success {initializing && 'is-loading'}"
                    on:click={() => setupRepo("new")}>Create a repository for the ledgers</button
                  >
                </div>
              {/if}
            {:else}
              <p>
                <strong
                  >{status.repo_mode === "existing"
                    ? "Using the existing repository"
                    : "Dedicated repository for the ledgers"}</strong
                >
                at <code>{status.repo_root}</code>.
                <button
                  type="button"
                  class="button is-small is-text"
                  on:click={() => (changingRepoSetup = !changingRepoSetup)}>Change</button
                >
              </p>

              {#if changingRepoSetup}
                <div class="field is-grouped mt-2">
                  <div class="control">
                    <button
                      class="button {status.repo_mode === 'existing' &&
                        'is-static'} {initializing && 'is-loading'}"
                      disabled={status.repo_mode === "existing"}
                      on:click={() => setupRepo("existing")}>Use existing parent repository</button
                    >
                  </div>
                  <div class="control">
                    <button
                      class="button {status.repo_mode === 'new' && 'is-static'} {initializing &&
                        'is-loading'}"
                      disabled={status.repo_mode === "new"}
                      on:click={() => setupRepo("new")}
                      >Dedicated repository for just the ledgers</button
                    >
                  </div>
                </div>
              {/if}
            {/if}
          </div>

          <div class="box px-3" style="max-width: 1024px;">
            <button
              type="button"
              class="button is-white is-fullwidth is-justify-content-flex-start px-0"
              on:click={() => (connectionOpen = !connectionOpen)}
            >
              <h4 class="title is-5 mb-0">
                Connection (optional)
                <span class="icon is-small ml-1">
                  <i class="fas {connectionOpen ? 'fa-angle-up' : 'fa-angle-down'}"></i>
                </span>
              </h4>
            </button>

            {#if connectionOpen}
              {#if !status.repo_detected}
                <p>Set up a local repository above before connecting a remote.</p>
              {:else}
                <table class="table is-fullwidth">
                  <tbody>
                    <tr>
                      <th>Remote</th>
                      <td>{status.remote_url || "Not connected"}</td>
                    </tr>
                    {#if status.remote_url}
                      <tr>
                        <th>Branch</th>
                        <td>{status.branch}</td>
                      </tr>
                      <tr>
                        <th>Status</th>
                        <td>
                          {#if status.ahead < 0 || status.behind < 0}
                            Unknown (couldn't reach remote)
                          {:else}
                            {status.ahead} ahead, {status.behind} behind
                          {/if}
                        </td>
                      </tr>
                    {/if}
                    {#if status.last_commit}
                      <tr>
                        <th>Last commit</th>
                        <td
                          >{shortHash(status.last_commit.hash)} &mdash; {status.last_commit.message}
                          ({relativeCommitTime(status.last_commit.when)})</td
                        >
                      </tr>
                    {/if}
                    {#if status.remote_url}
                      <tr>
                        <th>Secrets stored in</th>
                        <td
                          >{status.keyring_backend === "file"
                            ? "an encrypted local file (no OS keychain available)"
                            : "the OS keychain"}</td
                        >
                      </tr>
                    {/if}
                  </tbody>
                </table>

                <div class="field is-grouped">
                  {#if status.remote_url}
                    <div class="control">
                      <button
                        class="button is-success {pushing && 'is-loading'}"
                        on:click={() => push()}>Push</button
                      >
                    </div>
                    <div class="control">
                      <button class="button is-danger is-light" on:click={() => disconnect()}
                        >Disconnect</button
                      >
                    </div>
                  {/if}
                </div>
              {/if}

              {#if status.repo_detected && !status.remote_url}
                <hr />
                <h5 class="title is-6">Connect a remote</h5>

                <div class="field is-grouped">
                  <div class="control">
                    <button
                      class="button {connecting && 'is-loading'}"
                      on:click={() => startOAuth("github")}
                    >
                      <span class="icon"><i class="fab fa-github" /></span>
                      <span>Connect with GitHub</span>
                    </button>
                  </div>
                  <div class="control">
                    <button
                      class="button {connecting && 'is-loading'}"
                      on:click={() => startOAuth("gitlab")}
                    >
                      <span class="icon"><i class="fab fa-gitlab" /></span>
                      <span>Connect with GitLab</span>
                    </button>
                  </div>
                </div>

                <details>
                  <summary>Use a token instead (GitHub / GitLab)</summary>
                  <div class="field has-addons mt-2">
                    <div class="control">
                      <div class="select">
                        <select bind:value={tokenProvider}>
                          <option value="github">GitHub</option>
                          <option value="gitlab">GitLab</option>
                        </select>
                      </div>
                    </div>
                    <div class="control is-expanded">
                      <input
                        class="input"
                        type="password"
                        placeholder="Personal access token"
                        bind:value={tokenValue}
                      />
                    </div>
                    <div class="control">
                      <button
                        class="button {tokenSubmitting && 'is-loading'}"
                        disabled={!tokenValue}
                        on:click={() => submitManualToken()}>Connect</button
                      >
                    </div>
                  </div>
                </details>

                <details class="mt-3">
                  <summary>Other provider (self-hosted, Bitbucket, etc.)</summary>
                  <div class="field mt-2">
                    <label class="label" for="other-remote-url">Remote URL</label>
                    <input
                      id="other-remote-url"
                      class="input"
                      placeholder="https://example.com/user/repo.git"
                      bind:value={otherUrl}
                    />
                  </div>
                  <div class="field">
                    <label class="label" for="other-remote-branch">Branch</label>
                    <input id="other-remote-branch" class="input" bind:value={otherBranch} />
                  </div>
                  <div class="field">
                    <label class="label" for="other-remote-token">Token</label>
                    <input
                      id="other-remote-token"
                      class="input"
                      type="password"
                      bind:value={otherToken}
                    />
                  </div>
                  <div class="control">
                    <button
                      class="button {otherSubmitting && 'is-loading'}"
                      disabled={!otherUrl || !otherToken}
                      on:click={() => submitOtherProvider()}>Connect</button
                    >
                  </div>
                </details>
              {/if}
            {/if}
          </div>
        </div>
      </div>

      {#if status.repo_detected}
        <div class="columns">
          <div class="column is-3-widescreen is-2-fullhd is-4">
            <div class="box px-2 full-height overflow-y-auto">
              <aside class="menu">
                <FileTree
                  path=""
                  on:select={(e) => selectFile(e.detail)}
                  files={buildDirectoryTree(files)}
                  selectedFileName={selectedFile?.name}
                  hasUnsavedChanges={false}
                />
              </aside>
            </div>
          </div>
          <div class="column is-9-widescreen is-10-fullhd is-8">
            <div class="box px-3">
              {#if !selectedFile}
                <p>Select a file to view its history.</p>
              {:else if loadingCommits}
                <p>Loading...</p>
              {:else if _.isEmpty(commits)}
                <p>No history yet for {selectedFile.name}.</p>
              {:else}
                <table class="table is-fullwidth is-hoverable">
                  <thead>
                    <tr>
                      <th>Commit</th>
                      <th>Message</th>
                      <th>Author</th>
                      <th>When</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each commits as commit}
                      <tr class="is-clickable" on:click={() => viewDiff(commit)}>
                        <td><code>{shortHash(commit.hash)}</code></td>
                        <td>{commit.message}</td>
                        <td>{commit.author}</td>
                        <td>{relativeCommitTime(commit.when)}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          </div>
        </div>
      {/if}
    {/if}
  </div>
</div>

<Modal bind:active={deviceModalOpen} width="min(480px, 100vw)">
  <svelte:fragment slot="head" let:close>
    <p class="modal-card-title">Connect account</p>
    <button class="delete" aria-label="close" on:click={(e) => (cancelDeviceFlow(), close(e))} />
  </svelte:fragment>
  <div slot="body">
    <p>
      Enter this code at <a href={deviceVerificationURI} target="_blank">{deviceVerificationURI}</a
      >:
    </p>
    <p class="title is-3 has-text-centered mt-4">{deviceUserCode}</p>
    <p class="has-text-centered">Waiting for approval...</p>
  </div>
  <svelte:fragment slot="foot" let:close>
    <button class="button" on:click={(e) => (cancelDeviceFlow(), close(e))}>Cancel</button>
  </svelte:fragment>
</Modal>

<Modal bind:active={pickerOpen} width="min(640px, 100vw)">
  <svelte:fragment slot="head" let:close>
    <p class="modal-card-title">Select a repository</p>
    <button class="delete" aria-label="close" on:click={(e) => close(e)} />
  </svelte:fragment>
  <div slot="body">
    <div class="field">
      <input
        class="input"
        placeholder="Filter repositories..."
        bind:value={pickerQuery}
        disabled={pickerLoading}
      />
    </div>
    <div class="field">
      <label class="label" for="picker-branch">Branch</label>
      <input id="picker-branch" class="input" bind:value={pickerBranch} />
    </div>
    {#if pickerLoading}
      <p>Loading repositories...</p>
    {:else if _.isEmpty(filteredPickerRepos)}
      <p>No repositories found.</p>
    {:else}
      <ul class="du-menu du-menu-sm w-full p-0" style="max-height: 320px; overflow-y: auto;">
        {#each filteredPickerRepos as repo}
          <li>
            <button
              type="button"
              class="button is-fullwidth is-justify-content-flex-start"
              on:click={() => selectRepo(repo)}
            >
              <span>{repo.full_name}</span>
              {#if repo.private}
                <span class="tag is-light ml-1">private</span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
  <svelte:fragment slot="foot" let:close>
    <button class="button" on:click={(e) => close(e)}>Cancel</button>
  </svelte:fragment>
</Modal>

<DiffViewModal bind:open={diffOpen} oldFiles={diffOldFiles} newFiles={diffNewFiles} readonly />
