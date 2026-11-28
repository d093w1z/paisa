<script lang="ts">
  import {
    createEditor,
    editorState,
    focus,
    moveToEnd,
    moveToLine,
    updateContent
  } from "$lib/editor";
  import { insertTab } from "@codemirror/commands";
  import { ajax, buildDirectoryTree, type GitCommitInfo, type LedgerFile } from "$lib/utils";
  import { redo, undo } from "@codemirror/commands";
  import type { KeyBinding } from "@codemirror/view";
  import * as toast from "bulma-toast";
  import type { EditorView } from "codemirror";
  import { format } from "$lib/journal";
  import _ from "lodash";
  import { onMount } from "svelte";
  import { beforeNavigate, goto } from "$app/navigation";
  import type { PageData } from "./$types";
  import FileTree from "$lib/components/FileTree.svelte";
  import FileModal from "$lib/components/FileModal.svelte";
  import Modal from "$lib/components/Modal.svelte";
  import DiffViewModal from "$lib/components/DiffViewModal.svelte";
  import { relativeCommitTime, shortHash } from "$lib/git";
  import { page } from "$app/stores";

  export let data: PageData;
  let editorDom: Element;
  let editor: EditorView;
  let editorFileName = "";
  let filesMap: Record<string, LedgerFile> = {};
  let selectedFile: LedgerFile = null;
  let accounts: string[] = [];
  let commodities: string[] = [];
  let payees: string[] = [];
  let selectedVersion: string = null;
  let lineNumber = 0;

  let fileCommits: GitCommitInfo[] = [];

  let historyModalOpen = false;
  let historyLoading = false;

  let diffOpen = false;
  let diffOldFiles: LedgerFile[] = [];
  let diffNewFiles: LedgerFile[] = [];

  let commitMessageModalOpen = false;
  let commitMessage = "";

  function command(fn: Function) {
    return () => {
      fn();
      return true;
    };
  }

  const keybindings: readonly KeyBinding[] = [
    { key: "Tab", run: insertTab },
    {
      key: "Ctrl-s",
      run: command(save),
      preventDefault: true
    },
    {
      key: "Ctrl-I",
      run: command(pretty),
      preventDefault: true
    }
  ];

  let cancelled = false;
  beforeNavigate(async ({ cancel }) => {
    if ($editorState.hasUnsavedChanges) {
      const confirmed = confirm("You have unsaved changes. Are you sure you want to leave?");
      if (!confirmed) {
        cancel();
        cancelled = true;
      } else {
        $editorState = _.assign({}, $editorState, { hasUnsavedChanges: false });
      }
    }
  });

  async function navigate(url: string) {
    await goto(url, { noScroll: true });
    if (cancelled) {
      cancelled = false;
      return false;
    }
    return true;
  }

  onMount(async () => {
    loadFiles(data.name);
    const line = _.toNumber($page.url.hash.substring(1));
    if (_.isNumber(line)) {
      lineNumber = line;
    }
  });

  async function loadFiles(selectedFileName: string) {
    let files;
    ({ files, accounts, commodities, payees } = await ajax("/api/editor/files"));
    filesMap = _.fromPairs(_.map(files, (f) => [f.name, f]));
    if (!_.isEmpty(files)) {
      selectedFile = _.find(files, (f) => f.name == selectedFileName) || files[0];
    }
  }

  async function selectFile(file: LedgerFile) {
    const success = await navigate(`/ledger/editor/${encodeURIComponent(file.name)}`);
    if (success) {
      selectedFile = file;
    }
  }

  async function revert(version: string) {
    const { file } = await ajax("/api/editor/file", {
      method: "POST",
      body: JSON.stringify({ name: version }),
      background: true
    });

    updateContent(editor, file.content);
  }

  async function pretty() {
    const formatted = format(editor.state.doc.toString());
    if (formatted != editor.state.doc.toString()) {
      updateContent(editor, formatted);
    }
  }

  async function deleteBackups() {
    const { file } = await ajax("/api/editor/file/delete_backups", {
      method: "POST",
      body: JSON.stringify({ name: selectedFile.name }),
      background: true
    });

    selectedFile.versions = file.versions;
  }

  async function save(commitMessageOverride = "") {
    const doc = editor.state.doc;
    const { errors, saved, file, message } = await ajax("/api/editor/save", {
      method: "POST",
      body: JSON.stringify({
        name: selectedFile.name,
        content: doc.toString(),
        message: commitMessageOverride
      }),
      background: true
    });

    if (!saved) {
      toast.toast({
        message: `Failed to save ${selectedFile.name}. reason: ${message}`,
        type: "is-danger",
        duration: 10000
      });
      if (!_.isEmpty(errors)) {
        moveToLine(editor, errors[0].line_from);
      }
    } else {
      toast.toast({
        message: `Saved ${selectedFile.name}`,
        type: "is-success"
      });
      filesMap[file.name] = file;
      selectedFile = file;
      selectedVersion = null;
      $editorState = _.assign({}, $editorState, { hasUnsavedChanges: false });

      if (file.git_enabled) {
        await loadFileHistory();
      }
    }
  }

  function openCommitMessageModal() {
    commitMessage = "";
    commitMessageModalOpen = true;
  }

  async function saveWithMessage() {
    commitMessageModalOpen = false;
    await save(commitMessage);
  }

  async function loadFileHistory() {
    if (!selectedFile?.git_enabled) {
      fileCommits = [];
      return;
    }

    ({ commits: fileCommits } = await ajax(
      `/api/git/history?file=${encodeURIComponent(selectedFile.name)}`
    ));
  }

  function openHistory() {
    historyModalOpen = true;
    historyLoading = true;
    loadFileHistory().finally(() => {
      historyLoading = false;
    });
  }

  async function viewHistoryDiff(commit: GitCommitInfo) {
    const old = await ajax(
      `/api/git/content?file=${encodeURIComponent(selectedFile.name)}&commit=${commit.hash}`
    );

    if (old.error) {
      toast.toast({ message: old.error, type: "is-danger" });
      return;
    }

    diffOldFiles = [
      { type: "file", name: selectedFile.name, content: editor.state.doc.toString(), versions: [] }
    ];
    diffNewFiles = [{ type: "file", name: selectedFile.name, content: old.content, versions: [] }];
    historyModalOpen = false;
    diffOpen = true;
  }

  function restoreFromDiff(files: LedgerFile[]) {
    updateContent(editor, files[0].content);
    diffOpen = false;
    toast.toast({
      message: "Loaded the historical version into the editor — review and Save to keep it",
      type: "is-info"
    });
  }

  let lastHistoryFileName: string = null;
  $: if (selectedFile && selectedFile.name !== lastHistoryFileName) {
    lastHistoryFileName = selectedFile.name;
    loadFileHistory();
  }

  $: if (selectedFile) {
    if (
      !editor ||
      editorFileName !== selectedFile.name ||
      editor.state.doc.toString() != selectedFile.content
    ) {
      if (editor) {
        editor.destroy();
      }

      editor = createEditor(selectedFile.content, editorDom, {
        name: selectedFile.name,
        keybindings,
        autocompletions: {
          string: accounts,
          strong: payees,
          unit: commodities
        }
      });
      editorFileName = selectedFile.name;
      if (lineNumber > 0) {
        moveToLine(editor, lineNumber, true);
        focus(editor);
        lineNumber = 0;
      } else {
        moveToEnd(editor);
      }
    }
  }

  let modalOpen = false;
  function openCreateModal() {
    modalOpen = true;
  }

  async function createFile(destinationFile: string) {
    const { saved, message } = await ajax("/api/editor/save", {
      method: "POST",
      body: JSON.stringify({ name: destinationFile, content: "", operation: "create" }),
      background: true
    });

    if (saved) {
      toast.toast({
        message: `Created <b><a href="/ledger/editor/${encodeURIComponent(
          destinationFile
        )}">${destinationFile}</a></b>`,
        type: "is-success",
        duration: 5000
      });

      const success = await navigate(`/ledger/editor/${encodeURIComponent(destinationFile)}`);
      if (success) {
        await loadFiles(destinationFile);
      }
    } else {
      toast.toast({
        message: `Failed to create ${destinationFile}. reason: ${message}`,
        type: "is-danger",
        duration: 10000
      });
    }
  }
</script>

<FileModal bind:open={modalOpen} on:save={(e) => createFile(e.detail)} label="Create" help="" />

<section class="section tab-editor max-h-screen" style="padding-bottom: 0 !important">
  <div class="container is-fluid">
    <div class="columuns">
      <div class="column is-12 px-0 pt-0 mb-2">
        <div class="box p-3 is-flex is-align-items-center overflow-x-auto" style="width: 100%">
          <div class="field has-addons mb-0">
            <p class="control">
              <button
                class="button is-small is-link invertable is-light"
                disabled={$editorState.hasUnsavedChanges}
                on:click={(_e) => openCreateModal()}
              >
                <span class="icon is-small">
                  <i class="fas fa-file-circle-plus" />
                </span>
                <span>Create</span>
              </button>
            </p>
          </div>

          <div class="field has-addons ml-5 mb-0">
            <p class="control">
              <button
                class="button is-small"
                disabled={$editorState.hasUnsavedChanges == false}
                on:click={(_e) => save()}
              >
                <span class="icon is-small">
                  <i class="fas fa-floppy-disk" />
                </span>
                <span>Save</span>
              </button>
            </p>
            <p class="control">
              <button
                class="button is-small"
                disabled={$editorState.undoDepth == 0}
                on:click={(_e) => undo(editor)}
              >
                <span class="icon is-small">
                  <i class="fas fa-arrow-left" />
                </span>
                <span>Undo</span>
              </button>
            </p>
            <p class="control">
              <button
                class="button is-small"
                disabled={$editorState.redoDepth == 0}
                on:click={(_e) => redo(editor)}
              >
                <span>Redo</span>
                <span class="icon is-small">
                  <i class="fas fa-arrow-right" />
                </span>
              </button>
            </p>
            <p class="control">
              <button class="button is-small" on:click={(_e) => pretty()}>
                <span class="icon is-small">
                  <i class="fas fa-code" />
                </span>
                <span>Prettify</span>
              </button>
            </p>
          </div>

          {#if !_.isEmpty(selectedFile?.versions)}
            <div class="field has-addons ml-5 mb-0">
              <p class="control">
                <button
                  class="button is-small"
                  disabled={!selectedVersion}
                  on:click={(_e) => revert(selectedVersion)}
                >
                  <span class="icon is-small">
                    <i class="fas fa-clock-rotate-left" />
                  </span>
                  <span>Revert</span>
                </button>
              </p>

              <div class="control">
                <div class="select is-small">
                  <select bind:value={selectedVersion}>
                    {#each selectedFile.versions as version}
                      <option>{version}</option>
                    {/each}
                  </select>
                </div>
              </div>

              <p class="control">
                <button class="button is-small" on:click={(_e) => deleteBackups()}>
                  <span class="icon is-small">
                    <i class="fas fa-trash-can" />
                  </span>
                </button>
              </p>
            </div>
          {/if}

          {#if selectedFile?.git_enabled}
            <div class="field has-addons ml-5 mb-0">
              <p class="control">
                <button
                  class="button is-small"
                  disabled={!$editorState.hasUnsavedChanges}
                  on:click={(_e) => openCommitMessageModal()}
                >
                  <span class="icon is-small">
                    <i class="fas fa-pen-to-square" />
                  </span>
                  <span>Save with message</span>
                </button>
              </p>
              <p class="control">
                <button class="button is-small" on:click={(_e) => openHistory()}>
                  <span class="icon is-small">
                    <i class="fas fa-clock-rotate-left" />
                  </span>
                  <span>History</span>
                </button>
              </p>
            </div>

            {#if !_.isEmpty(fileCommits)}
              <div class="ml-3 is-size-7 has-text-grey">
                Last commit: <code>{shortHash(fileCommits[0].hash)}</code>
                {fileCommits[0].message} ({relativeCommitTime(fileCommits[0].when)})
              </div>
            {/if}
          {/if}

          {#if $editorState.errors.length > 0}
            <div class="control ml-5">
              <a on:click={(_e) => moveToLine(editor, $editorState.errors[0].line_from)}
                ><span class="ml-1 tag invertable is-danger is-light"
                  >{$editorState.errors.length} error(s) found</span
                ></a
              >
            </div>
          {/if}
        </div>
      </div>
    </div>
    <div class="columns">
      <div class="column is-3-widescreen is-2-fullhd is-4">
        <div class="box px-2 full-height overflow-y-auto">
          <aside class="menu">
            <FileTree
              path=""
              on:select={(e) => selectFile(e.detail)}
              files={buildDirectoryTree(_.values(filesMap))}
              selectedFileName={selectedFile?.name}
              hasUnsavedChanges={$editorState.hasUnsavedChanges}
            />
          </aside>
        </div>
      </div>
      <div class="column is-6-widescreen is-6-fullhd is-8">
        <div class="box py-0">
          <div class="editor" bind:this={editorDom} />
        </div>
      </div>
      <div class="column is-3-widescreen is-4-fullhd is-hidden-touch is-hidden-desktop-only">
        {#if !_.isEmpty($editorState.output)}
          <pre class="box px-3 full-height">{$editorState.output}</pre>
        {/if}
      </div>
    </div>
  </div>
</section>

<Modal bind:active={commitMessageModalOpen} width="min(520px, 100vw)">
  <svelte:fragment slot="head" let:close>
    <p class="modal-card-title">Commit message</p>
    <button class="delete" aria-label="close" on:click={(e) => close(e)} />
  </svelte:fragment>
  <div slot="body">
    <div class="field">
      <div class="control">
        <textarea
          class="textarea"
          rows="3"
          placeholder={`paisa: update ${selectedFile?.name}`}
          bind:value={commitMessage}
        />
      </div>
    </div>
  </div>
  <svelte:fragment slot="foot" let:close>
    <button class="button" on:click={(e) => close(e)}>Cancel</button>
    <button class="button is-success" on:click={() => saveWithMessage()}>Save</button>
  </svelte:fragment>
</Modal>

<Modal bind:active={historyModalOpen} width="min(700px, 100vw)">
  <svelte:fragment slot="head" let:close>
    <p class="modal-card-title">History — {selectedFile?.name}</p>
    <button class="delete" aria-label="close" on:click={(e) => close(e)} />
  </svelte:fragment>
  <div slot="body">
    {#if historyLoading}
      <p>Loading...</p>
    {:else if _.isEmpty(fileCommits)}
      <p>No history yet for {selectedFile?.name}.</p>
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
          {#each fileCommits as commit}
            <tr class="is-clickable" on:click={() => viewHistoryDiff(commit)}>
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
  <svelte:fragment slot="foot" let:close>
    <button class="button" on:click={(e) => close(e)}>Close</button>
  </svelte:fragment>
</Modal>

<DiffViewModal
  bind:open={diffOpen}
  oldFiles={diffOldFiles}
  newFiles={diffNewFiles}
  saveLabel="Restore this version"
  on:save={(e) => restoreFromDiff(e.detail)}
/>
