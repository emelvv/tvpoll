import {request, message, pollState, dateTime, element} from "/common.js";

const $ = selector => document.querySelector(selector);
let token = "";
let selectedPoll;
let selectedRequest = 0;
let createKey;
let createPayload;
let createPending = false;

function localDate(date) {
  const pad = number => String(number).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

const now = new Date();
$("#opens-at").value = localDate(now);
$("#closes-at").value = localDate(new Date(now.getTime() + 5 * 60 * 1000));
$("#timezone").textContent = `Время вашего браузера: ${Intl.DateTimeFormat().resolvedOptions().timeZone}.`;

async function listPolls() {
  message($("#polls-message"), "Загружаем опросы…", "info");
  const {value} = await request("/api/admin/polls", {token});
  const list = $("#poll-list");
  list.replaceChildren();
  const polls = [...value.polls].sort((left, right) => Date.parse(right.opens_at) - Date.parse(left.opens_at));
  if (polls.length === 0) list.append(element("p", "empty muted", "Здесь появится ваш первый опрос. Создайте его в форме слева."));
  for (const poll of polls) {
    const state = pollState(poll);
    const item = element("button", "poll-list-item");
    item.type = "button";
    item.classList.toggle("selected", selectedPoll?.id === poll.id);
    item.setAttribute("aria-pressed", String(selectedPoll?.id === poll.id));
    const top = element("span", "poll-list-meta");
    top.append(element("span", `badge ${state.kind}`, state.text), element("span", "small muted", dateTime(poll.opens_at)));
    item.append(top, element("span", "poll-list-question", poll.question), element("span", "small muted", `${poll.type === "single" ? "Один ответ" : "Несколько ответов"} · ${poll.options.length} вариантов`));
    item.addEventListener("click", () => {
      for (const sibling of list.querySelectorAll("button")) { sibling.classList.remove("selected"); sibling.setAttribute("aria-pressed", "false"); }
      item.classList.add("selected");
      item.setAttribute("aria-pressed", "true");
      loadResults(poll);
    });
    list.append(item);
  }
  message($("#polls-message"));
}

async function loadResults(poll) {
  selectedPoll = poll;
  const requestNumber = ++selectedRequest;
  $("#results-panel").hidden = false;
  $("#results-heading").textContent = poll.question;
  $("#vote-link").value = `${location.origin}/?poll=${encodeURIComponent(poll.id)}`;
  $("#results-content").hidden = true;
  message($("#results-message"), "Получаем результаты…", "info");
  try {
    const {value} = await request(`/api/admin/polls/${encodeURIComponent(poll.id)}/results`, {token});
    if (requestNumber !== selectedRequest || !token) return;
    $("#total-votes").textContent = new Intl.NumberFormat("ru-RU").format(value.total_votes);
    const bars = $("#result-bars");
    bars.replaceChildren();
    for (const [index, option] of value.poll.options.entries()) {
      const count = value.option_counts[index] || 0;
      const ratio = value.total_votes > 0 ? count / value.total_votes : 0;
      const row = element("div", "result-row");
      const text = element("div", "result-label");
      text.append(element("span", "", option), element("span", "result-number", `${new Intl.NumberFormat("ru-RU").format(count)} · ${(ratio * 100).toLocaleString("ru-RU", {maximumFractionDigits: 1})}%`));
      const meter = element("meter", "result-meter");
      meter.min = 0;
      meter.max = 1;
      meter.value = ratio;
      meter.setAttribute("aria-label", `${option}: ${(ratio * 100).toFixed(1)}% участников`);
      row.append(text, meter);
      bars.append(row);
    }
    $("#results-note").textContent = value.poll.type === "multiple" ? "Проценты — доля проголосовавших, выбравших вариант. При нескольких ответах сумма может превышать 100%." : "Проценты — доля проголосовавших, выбравших вариант.";
    $("#observed-at").textContent = `Обновлено ${dateTime(value.observed_at)}. ${value.consistent === false ? "Во время голосования результат предварительный. Итог — после завершения и обработки всех принятых запросов." : ""}`;
    $("#results-content").hidden = false;
    message($("#results-message"));
  } catch (error) { if (requestNumber === selectedRequest) message($("#results-message"), error.message); }
}

$("#login-form").addEventListener("submit", async event => {
  event.preventDefault();
  const button = event.currentTarget.querySelector("button");
  token = $("#admin-token").value.trim();
  $("#admin-token").value = "";
  button.disabled = true;
  message($("#login-message"), "Проверяем доступ…", "info");
  try {
    await listPolls();
    $("#login-panel").hidden = true;
    $("#dashboard").hidden = false;
    $("#logout").hidden = false;
    $("#question").focus();
  } catch (error) { token = ""; message($("#login-message"), error.message); }
  finally { button.disabled = false; }
});

$("#logout").addEventListener("click", () => {
  token = "";
  selectedPoll = undefined;
  selectedRequest++;
  createKey = undefined;
  createPayload = undefined;
  $("#poll-list").replaceChildren();
  $("#result-bars").replaceChildren();
  $("#vote-link").value = "";
  $("#results-panel").hidden = true;
  $("#dashboard").hidden = true;
  $("#logout").hidden = true;
  $("#login-panel").hidden = false;
  message($("#login-message"));
  $("#admin-token").focus();
});

$("#poll-type").addEventListener("change", () => { $("#choice-limits").hidden = $("#poll-type").value !== "multiple"; });

$("#create-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (createPending) return;
  const options = $("#options").value.split("\n").map(option => option.trim()).filter(Boolean);
  const type = $("#poll-type").value;
  const min = type === "single" ? 1 : Number($("#min-choices").value);
  const max = type === "single" ? 1 : Number($("#max-choices").value);
  if (options.length < 2 || options.length > 32 || options.some(option => option.length > 200) || new Set(options).size !== options.length) { message($("#create-message"), "Добавьте от 2 до 32 разных вариантов, не длиннее 200 символов каждый."); return; }
  if (!Number.isInteger(min) || !Number.isInteger(max) || min < 1 || min > max || max > options.length) { message($("#create-message"), "Количество ответов должно быть от 1 до количества вариантов, минимум не больше максимума."); return; }
  const opensAt = new Date($("#opens-at").value);
  const closesAt = new Date($("#closes-at").value);
  if (!Number.isFinite(opensAt.getTime()) || !Number.isFinite(closesAt.getTime()) || closesAt <= opensAt) { message($("#create-message"), "Время завершения должно быть позже начала."); return; }
  const body = {question: $("#question").value.trim(), type, options, min_choices: min, max_choices: max, opens_at: opensAt.toISOString(), closes_at: closesAt.toISOString()};
  const serialized = JSON.stringify(body);
  if (serialized !== createPayload) { createKey = crypto.randomUUID(); createPayload = serialized; }
  createPending = true;
  $("#create-button").disabled = true;
  message($("#create-message"), "Создаём опрос…", "info");
  try {
    const {value: poll} = await request("/api/admin/polls", {method: "POST", token, body, idempotencyKey: createKey});
    createKey = undefined;
    createPayload = undefined;
    selectedPoll = poll;
    message($("#create-message"), "Опрос создан. Ссылка на голосование — в блоке результатов.", "success");
    // Creation has already committed. A failed list refresh must not turn a
    // known success into an apparent creation failure and invite another poll.
    await listPolls().catch(error => message($("#polls-message"), `Опрос создан, но список не обновился: ${error.message}`));
    await loadResults(poll);
  } catch (error) { message($("#create-message"), error.message); }
  finally { createPending = false; $("#create-button").disabled = false; }
});

$("#reload-polls").addEventListener("click", () => listPolls().catch(error => message($("#polls-message"), error.message)));
$("#reload-results").addEventListener("click", () => { if (selectedPoll) loadResults(selectedPoll); });
$("#copy-link").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText($("#vote-link").value); message($("#results-message"), "Ссылка скопирована.", "success"); }
  catch { $("#vote-link").select(); message($("#results-message"), "Выделенная ссылка готова к копированию.", "info"); }
});
