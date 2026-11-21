<script lang="ts">
  // Review queue UI. All logic (rewriting entries, learning rules, declaring accounts) lives in
  // ledger-writer; this page only collects the choice and posts it through /api/writer.
  import {
    ajax,
    formatCurrency,
    type ReviewGroup,
    type ReviewItem,
    type ReviewResult,
    type WriterError,
    type WriterTxn
  } from "$lib/utils";
  import { toast } from "bulma-toast";
  import _ from "lodash";
  import { onMount } from "svelte";

  const CATEGORY_ROOTS = [
    "Expenses:",
    "Income:",
    "Assets:Transfers:",
    "Assets:Investments:",
    "Assets:Receivables:",
    "Liabilities:EMI:"
  ];

  type Row = ReviewGroup & { name: string; category: string; learn: boolean; open: boolean };

  let rows: Row[] = [];
  let items: ReviewItem[] = [];
  let accounts: string[] = [];
  let categories: string[] = [];
  let error = "";
  let saving: Record<string, boolean> = {};
  let filter = "";

  function failed<T>(res: T | WriterError): res is WriterError {
    return !!res && typeof res === "object" && "error" in res;
  }

  function notify(message: string, ok = true) {
    toast({ message, type: ok ? "is-success" : "is-danger", duration: ok ? 3000 : 10000 });
  }

  async function load() {
    const [groups, all, accts] = await Promise.all([
      ajax("/api/writer/review?group=payee"),
      ajax("/api/writer/review"),
      ajax("/api/writer/accounts")
    ]);
    if (failed(groups) || failed(all) || failed(accts)) {
      error = [groups, all, accts].filter(failed).map((e) => e.error)[0];
      return;
    }
    error = "";
    items = all as ReviewItem[];
    accounts = accts;
    categories = accounts.filter((a) => CATEGORY_ROOTS.some((r) => a.startsWith(r)));
    const byPayee = _.groupBy(items, (i) => (i.payee_raw || "").toLowerCase());
    rows = groups.map((g) => {
      const first = byPayee[(g.payee_raw || "").toLowerCase()]?.[0];
      return { ...g, name: first?.payee || g.payee_raw, category: "", learn: true, open: false };
    });
  }

  function confirmNew(account: string): boolean | null {
    if (accounts.includes(account)) return false;
    if (!CATEGORY_ROOTS.some((r) => account.startsWith(r))) {
      notify(
        `${account} doesn't exist. New categories must start with ${CATEGORY_ROOTS.join(", ")}`,
        false
      );
      return null;
    }
    return confirm(`Create new category ${account}?`) ? true : null;
  }

  async function post(
    route: "/api/writer/review" | "/api/writer/review/:id",
    body: object,
    id?: number
  ) {
    const options = { method: "POST", body: JSON.stringify(body) };
    return id === undefined
      ? ((await ajax("/api/writer/review", options)) as ReviewResult | WriterError)
      : await ajax("/api/writer/review/:id", options, { id: String(id) });
  }

  type Entry = {
    id?: number;
    payee_raw?: string;
    account: string;
    payee?: string;
    make_rule?: boolean;
    new_account?: boolean;
  };

  // A group's entries: rows given their own category (expanded view) first, then the group category
  // (if any) for the rest. The writer applies them in order, in one commit.
  function entriesFor(row: Row, account: string, learn: boolean): Entry[] {
    const payee = row.name.trim() || undefined;
    const own: Entry[] = itemsOf(row)
      .filter((i) => i.reason === "uncategorized" && (itemCategory[i.id] || "").trim())
      .map((i) => ({ id: i.id, account: itemCategory[i.id].trim(), payee }));
    account = account.trim();
    return account ? [...own, { payee_raw: row.payee_raw, account, payee, make_rule: learn }] : own;
  }

  async function runBatch(entries: Entry[], busy: string[]) {
    if (entries.length === 0) return notify("Pick a category first", false);
    const isNew: Record<string, boolean> = {};
    for (const a of _.uniq(entries.map((e) => e.account))) {
      const n = confirmNew(a);
      if (n === null) return;
      isNew[a] = n;
    }
    busy.forEach((k) => (saving[k] = true));
    try {
      const res = await ajax("/api/writer/review/batch", {
        method: "POST",
        body: JSON.stringify({
          items: entries.map((e) => ({ ...e, new_account: isNew[e.account] }))
        })
      });
      if (failed(res)) return notify(`Nothing saved: ${res.error}`, false);
      const rules = res.rules?.length ? `, ${res.rules.length} rule(s) learned` : "";
      notify(`${res.resolved.length} transactions categorized${rules}`);
      entries.forEach((e) => e.id !== undefined && delete itemCategory[e.id]);
      selected = {};
    } finally {
      busy.forEach((k) => (saving[k] = false));
      await load();
    }
  }

  const saveGroup = (row: Row) =>
    runBatch(entriesFor(row, row.category, row.learn), [row.payee_raw]);

  // Bulk: ticked groups get the bulk category, or their own category when the bulk one is empty.
  let selected: Record<string, boolean> = {};
  let bulkCategory = "";
  let bulkLearn = false;

  function applyBulk() {
    const picked = rows.filter((r) => selected[r.payee_raw]);
    const missing = picked.filter((r) => !bulkCategory.trim() && !r.category.trim());
    if (missing.length)
      return notify(`No category for ${missing.map((r) => r.payee_raw).join(", ")}`, false);
    runBatch(
      picked.flatMap((r) =>
        entriesFor(r, bulkCategory.trim() || r.category, bulkCategory.trim() ? bulkLearn : r.learn)
      ),
      picked.map((r) => r.payee_raw)
    );
  }

  async function saveItem(item: ReviewItem, account: string, row: Row) {
    account = (account || "").trim();
    if (!account) return notify("Pick a category first", false);
    const isNew = confirmNew(account);
    if (isNew === null) return;
    const res = await post(
      "/api/writer/review/:id",
      { account, payee: row.name.trim() || undefined, new_account: isNew },
      item.id
    );
    if (failed(res)) return notify(res.error, false);
    notify(`${item.date} ${item.amount} → ${account}`);
    await load();
  }

  async function unknownAccount(item: ReviewItem, action: "retry" | "void") {
    const res = await post("/api/writer/review/:id", { [action]: true }, item.id);
    if (failed(res)) return notify(res.error, false);
    notify(action === "retry" ? "Imported" : "Dismissed");
    await load();
  }

  const itemsOf = (row: Row) =>
    items.filter((i) => (i.payee_raw || "").toLowerCase() === (row.payee_raw || "").toLowerCase());

  let itemCategory: Record<number, string> = {};

  $: shown = rows.filter(
    (r) => !filter || (r.payee_raw || "").toLowerCase().includes(filter.toLowerCase())
  );
  $: openCount = _.sumBy(rows, (r) => r.count);
  $: selectable = shown.filter((r) => r.reason === "uncategorized");
  $: picked = rows.filter((r) => selected[r.payee_raw]);
  $: allPicked = selectable.length > 0 && selectable.every((r) => selected[r.payee_raw]);
  function toggleAll() {
    const on = !allPicked;
    selectable.forEach((r) => (selected[r.payee_raw] = on));
  }

  // Find & re-categorize: any transaction, categorized or not (not hand-split ones).
  let find = { q: "", account: "", from: "", to: "" };
  let found: WriterTxn[] = [];
  let searched = false;
  let pick: Record<number, boolean> = {};
  let newCategory: Record<number, string> = {};
  let findCategory = "";
  let findPayee = "";

  async function search() {
    const enc = (v: string) => encodeURIComponent(v.trim());
    const res = await ajax(
      "/api/writer/txns?q=:q&account=:account&date_from=:from&date_to=:to",
      {},
      { q: enc(find.q), account: enc(find.account), from: enc(find.from), to: enc(find.to) }
    );
    if (failed(res)) return notify(res.error, false);
    found = res;
    searched = true;
    pick = {};
    newCategory = {};
  }

  async function recategorize() {
    const all = findCategory.trim();
    const entries = found
      .map((t) => ({ t, account: (newCategory[t.id] || "").trim() || (pick[t.id] ? all : "") }))
      .filter((x) => x.account && x.account !== x.t.category);
    if (entries.length === 0)
      return notify("Tick rows and give a category (or type one per row)", false);
    const isNew: Record<string, boolean> = {};
    for (const a of _.uniq(entries.map((e) => e.account))) {
      const n = confirmNew(a);
      if (n === null) return;
      isNew[a] = n;
    }
    const res = await ajax("/api/writer/review/batch", {
      method: "POST",
      body: JSON.stringify({
        items: entries.map((e) => ({
          txn_id: e.t.id,
          account: e.account,
          payee: findPayee.trim() || undefined,
          new_account: isNew[e.account]
        }))
      })
    });
    if (failed(res)) return notify(`Nothing saved: ${res.error}`, false);
    notify(`${res.recategorized?.length ?? 0} transactions re-categorized`);
    await Promise.all([search(), load()]);
  }

  $: pickedTxns = found.filter((t) => pick[t.id]).length;
  $: allTxnsPicked = found.length > 0 && found.every((t) => pick[t.id]);

  onMount(load);
</script>

<datalist id="review-categories">
  {#each categories as c}
    <option value={c} />
  {/each}
</datalist>

<section class="section">
  <div class="container is-fluid">
    {#if error}
      <div class="notification is-warning">
        <p><strong>Review queue unavailable:</strong> {error}</p>
      </div>
    {:else}
      <div class="level">
        <div class="level-left">
          <div class="level-item">
            <p>
              <strong>{openCount}</strong> transactions in <strong>{rows.length}</strong> groups need
              a category
            </p>
          </div>
        </div>
        <div class="level-right">
          <div class="level-item">
            <input class="input is-small" placeholder="Filter payee" bind:value={filter} />
          </div>
          <div class="level-item">
            <button class="button is-small" on:click={load}>
              <span class="icon is-small"><i class="fas fa-rotate" /></span><span>Refresh</span>
            </button>
          </div>
        </div>
      </div>

      {#if picked.length}
        <div class="box py-2 px-3 mb-3">
          <div class="field is-grouped is-align-items-center">
            <p class="control">
              <strong>{picked.length}</strong> groups, {_.sumBy(picked, (r) => r.count)} transactions
            </p>
            <p class="control is-expanded">
              <input
                class="input is-small"
                list="review-categories"
                placeholder="Category for all (empty: each row's own)"
                bind:value={bulkCategory}
                on:keydown={(e) => e.key === "Enter" && applyBulk()}
              />
            </p>
            <label class="control checkbox is-size-7" title="Learn a rule for each ticked payee">
              <input type="checkbox" bind:checked={bulkLearn} disabled={!bulkCategory.trim()} /> Learn
            </label>
            <p class="control">
              <button class="button is-small is-link" on:click={applyBulk}>Apply</button>
            </p>
            <p class="control">
              <button class="button is-small" on:click={() => (selected = {})}>Clear</button>
            </p>
          </div>
        </div>
      {/if}

      {#if rows.length === 0}
        <div class="notification is-success is-light">Nothing to review. 🎉</div>
      {/if}

      <table class="table is-fullwidth is-narrow is-hoverable">
        <thead>
          <tr>
            <th
              ><input
                type="checkbox"
                title="Select all shown"
                checked={allPicked}
                on:change={toggleAll}
              /></th
            >
            <th>Payee (as on statement)</th>
            <th class="has-text-right">Count</th>
            <th class="has-text-right">Total</th>
            <th>Name to show</th>
            <th>Category</th>
            <th
              title="Save a rule so future transactions from this payee are categorized automatically"
              >Learn</th
            >
            <th />
          </tr>
        </thead>
        <tbody>
          {#each shown as row (row.reason + row.payee_raw)}
            <tr>
              <td class="is-flex is-align-items-center">
                {#if row.reason === "uncategorized"}
                  <input type="checkbox" bind:checked={selected[row.payee_raw]} />
                {/if}
                <button class="button is-small is-ghost" on:click={() => (row.open = !row.open)}>
                  <span class="icon is-small"
                    ><i class="fas {row.open ? 'fa-chevron-down' : 'fa-chevron-right'}" /></span
                  >
                </button>
              </td>
              <td>
                <strong>{row.payee_raw || "—"}</strong>
                {#if row.reason !== "uncategorized"}<span class="tag is-warning is-light ml-1"
                    >{row.reason}</span
                  >{/if}
                <br /><span class="is-size-7 has-text-grey">{row.accounts.join(", ")}</span>
              </td>
              <td class="has-text-right">{row.count}</td>
              <td class="has-text-right is-family-monospace"
                >{formatCurrency(Number(row.total), 2)}</td
              >
              {#if row.reason === "uncategorized"}
                <td><input class="input is-small" bind:value={row.name} /></td>
                <td>
                  <input
                    class="input is-small"
                    list="review-categories"
                    placeholder="Expenses:Food:Dining"
                    bind:value={row.category}
                    on:keydown={(e) => e.key === "Enter" && saveGroup(row)}
                  />
                </td>
                <td class="has-text-centered"><input type="checkbox" bind:checked={row.learn} /></td
                >
                <td>
                  <button
                    class="button is-small is-link"
                    class:is-loading={saving[row.payee_raw]}
                    on:click={() => saveGroup(row)}
                    >Save {row.count > 1 ? `all ${row.count}` : ""}</button
                  >
                </td>
              {:else}
                <td colspan="4" class="is-size-7">Expand to retry or dismiss</td>
              {/if}
            </tr>
            {#if row.open}
              {#each itemsOf(row) as item (item.id)}
                <tr class="has-background-white-ter is-size-7">
                  <td />
                  <td colspan="2"
                    >{item.date || ""} · {item.account || ""}<br /><span class="has-text-grey"
                      >{item.raw || ""}</span
                    ></td
                  >
                  <td class="has-text-right is-family-monospace"
                    >{item.amount ? formatCurrency(Number(item.amount), 2) : ""}</td
                  >
                  {#if item.reason === "unknown_account"}
                    <td colspan="3"
                      >Add this card/account to <code>accounts.yaml</code>, then retry.</td
                    >
                    <td>
                      <button class="button is-small" on:click={() => unknownAccount(item, "retry")}
                        >Retry</button
                      >
                      <button
                        class="button is-small is-danger is-light"
                        on:click={() => unknownAccount(item, "void")}>Dismiss</button
                      >
                    </td>
                  {:else}
                    <td colspan="2">
                      <input
                        class="input is-small"
                        list="review-categories"
                        placeholder="Own category (empty: group's)"
                        title="Save all / Apply saves this row here, the rest with the group category"
                        bind:value={itemCategory[item.id]}
                      />
                    </td>
                    <td />
                    <td
                      ><button
                        class="button is-small"
                        on:click={() => saveItem(item, itemCategory[item.id], row)}>Save one</button
                      ></td
                    >
                  {/if}
                </tr>
              {/each}
            {/if}
          {/each}
        </tbody>
      </table>
    {/if}

    <h2 class="title is-5 mt-6" id="recategorize">Find &amp; re-categorize</h2>
    <p class="is-size-7 has-text-grey mb-3">
      Change the category of any transaction, including ones already categorized. Search by payee,
      then tick rows and give one category, or type a category on individual rows.
    </p>
    <div class="field is-grouped is-grouped-multiline">
      <p class="control is-expanded">
        <input
          class="input is-small"
          placeholder="Payee or statement text (e.g. sanika, amazon)"
          bind:value={find.q}
          on:keydown={(e) => e.key === "Enter" && search()}
        />
      </p>
      <p class="control">
        <input
          class="input is-small"
          list="review-categories"
          placeholder="Account or category"
          bind:value={find.account}
          on:keydown={(e) => e.key === "Enter" && search()}
        />
      </p>
      <p class="control"><input class="input is-small" type="date" bind:value={find.from} /></p>
      <p class="control"><input class="input is-small" type="date" bind:value={find.to} /></p>
      <p class="control">
        <button class="button is-small is-link" on:click={search}>Search</button>
      </p>
    </div>

    {#if searched}
      {#if found.length === 0}
        <div class="notification is-light">No transactions match.</div>
      {:else}
        <div class="box py-2 px-3 mb-3">
          <div class="field is-grouped is-align-items-center">
            <p class="control">
              <strong>{pickedTxns}</strong> of {found.length} ticked, total
              {formatCurrency(
                _.sumBy(
                  found.filter((t) => pick[t.id]),
                  (t) => Number(t.amount)
                ),
                2
              )}
            </p>
            <p class="control is-expanded">
              <input
                class="input is-small"
                list="review-categories"
                placeholder="New category for ticked rows"
                bind:value={findCategory}
              />
            </p>
            <p class="control">
              <input
                class="input is-small"
                placeholder="Rename payee (optional)"
                bind:value={findPayee}
              />
            </p>
            <p class="control">
              <button class="button is-small is-link" on:click={recategorize}>Re-categorize</button>
            </p>
          </div>
        </div>
        <table class="table is-fullwidth is-narrow is-hoverable is-size-7">
          <thead>
            <tr>
              <th
                ><input
                  type="checkbox"
                  checked={allTxnsPicked}
                  on:change={() => {
                    const on = !allTxnsPicked;
                    found.forEach((t) => (pick[t.id] = on));
                  }}
                /></th
              >
              <th>Date</th>
              <th>Payee</th>
              <th>Account</th>
              <th class="has-text-right">Amount</th>
              <th>Category</th>
              <th>New category (this row)</th>
            </tr>
          </thead>
          <tbody>
            {#each found as t (t.id)}
              <tr>
                <td><input type="checkbox" bind:checked={pick[t.id]} /></td>
                <td>{t.date}</td>
                <td title={t.raw || ""}>{t.payee}</td>
                <td>{t.account}</td>
                <td class="has-text-right is-family-monospace"
                  >{formatCurrency(Number(t.amount), 2)}</td
                >
                <td>{t.category}</td>
                <td>
                  <input
                    class="input is-small"
                    list="review-categories"
                    placeholder={pick[t.id] && findCategory ? findCategory : ""}
                    bind:value={newCategory[t.id]}
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/if}
  </div>
</section>
