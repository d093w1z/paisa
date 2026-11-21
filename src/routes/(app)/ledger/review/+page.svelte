<script lang="ts">
  // Review queue UI. All logic (rewriting entries, learning rules, declaring accounts) lives in
  // ledger-writer; this page only collects the choice and posts it through /api/writer.
  import {
    ajax,
    formatCurrency,
    type ReviewGroup,
    type ReviewItem,
    type ReviewResult,
    type WriterError
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

  // Save all: rows given their own category (expanded view) are saved one by one, then the group
  // category (if any) takes the rest, learning a rule when ticked.
  async function saveGroup(row: Row) {
    const account = row.category.trim();
    const own = itemsOf(row)
      .map((i) => ({ item: i, account: (itemCategory[i.id] || "").trim() }))
      .filter((x) => x.account && x.item.reason === "uncategorized");
    if (!account && own.length === 0) return notify("Pick a category first", false);

    const isNew: Record<string, boolean> = {};
    for (const a of _.uniq([...own.map((x) => x.account), ...(account ? [account] : [])])) {
      const n = confirmNew(a);
      if (n === null) return;
      isNew[a] = n;
    }
    const payee = row.name.trim() || undefined;
    saving[row.payee_raw] = true;
    try {
      for (const x of own) {
        const res = await post(
          "/api/writer/review/:id",
          { account: x.account, payee, new_account: isNew[x.account] },
          x.item.id
        );
        if (failed(res)) return notify(`${x.item.date} ${x.item.amount}: ${res.error}`, false);
        delete itemCategory[x.item.id];
      }
      let msg = own.length ? `${own.length} saved individually` : "";
      if (account) {
        const res = await post("/api/writer/review", {
          payee_raw: row.payee_raw,
          payee,
          account,
          make_rule: row.learn,
          new_account: isNew[account]
        });
        if (failed(res)) return notify(res.error, false);
        msg = [msg, `${res.resolved.length} → ${account}${row.learn ? " (rule saved)" : ""}`]
          .filter(Boolean)
          .join(", ");
      }
      notify(`${row.name}: ${msg}`);
    } finally {
      saving[row.payee_raw] = false;
      await load();
    }
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

      {#if rows.length === 0}
        <div class="notification is-success is-light">Nothing to review. 🎉</div>
      {/if}

      <table class="table is-fullwidth is-narrow is-hoverable">
        <thead>
          <tr>
            <th />
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
              <td>
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
                        title="Save all saves this row here, the rest with the group category"
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
  </div>
</section>
