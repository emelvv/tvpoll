import {request, message, pollState, dateTime, element} from "/common.js";

const form = document.querySelector("#vote-form");
const button = document.querySelector("#vote-button");
const notice = document.querySelector("#vote-message");
const heading = document.querySelector("#poll-heading");
const badge = document.querySelector("#poll-state");
let poll;
let sessionReady = false;
let submitting = false;
let voted = false;

async function establishSession() {
  const retry = document.querySelector("#session-retry");
  retry.disabled = true;
  try {
    await request("/api/session", {method: "POST"});
    sessionReady = true;
    retry.hidden = true;
    message(notice);
  } catch (error) { retry.hidden = false; message(notice, error.message); }
  finally { retry.disabled = false; updateState(); }
}

function updateState() {
  if (!poll) return;
  const state = pollState(poll);
  badge.textContent = state.text;
  badge.className = `badge ${state.kind}`;
  const selected = form.querySelectorAll("input:checked").length;
  button.disabled = submitting || voted || !sessionReady || !state.open || selected < poll.min_choices || selected > poll.max_choices;
  for (const input of form.querySelectorAll("input")) input.disabled = submitting || voted || !state.open;
  const remaining = Math.max(0, Math.ceil((Date.parse(poll.closes_at) - Date.now()) / 1000));
  document.querySelector("#poll-time").textContent = state.open ? `До завершения: ${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, "0")}` : state.kind === "future" ? `Начало: ${dateTime(poll.opens_at)}` : `Завершён: ${dateTime(poll.closes_at)}`;
}

async function initialize() {
  const id = new URLSearchParams(location.search).get("poll");
  if (!id) {
    heading.textContent = "Ваш вопрос — по ссылке из эфира";
    badge.textContent = "Добро пожаловать";
    document.querySelector("#empty-poll").hidden = false;
    return;
  }
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) {
    heading.textContent = "Некорректная ссылка";
    badge.textContent = "Опрос недоступен";
    message(notice, "Проверьте ссылку или отсканируйте QR-код ещё раз.");
    return;
  }
  try {
    poll = (await request(`/api/polls/${encodeURIComponent(id)}`)).value;
    heading.textContent = poll.question;
    document.querySelector("#poll-instruction").textContent = poll.type === "single" ? "Выберите один вариант ответа." : `Выберите от ${poll.min_choices} до ${poll.max_choices} вариантов.`;
    for (const [index, option] of poll.options.entries()) {
      const label = element("label", "choice");
      const input = element("input");
      input.type = poll.type === "single" ? "radio" : "checkbox";
      input.name = "choice";
      input.value = index;
      const text = element("span", "choice-text", option);
      label.append(input, text, element("span", "choice-number", String(index + 1).padStart(2, "0")));
      document.querySelector("#choices").append(label);
    }
    form.hidden = false;
    updateState();
    setInterval(updateState, 1000);
    await establishSession();
  } catch (error) {
    if (!poll) { heading.textContent = "Не удалось открыть опрос"; badge.textContent = "Недоступен"; }
    message(notice, error.message);
  }
}

form.addEventListener("change", () => { message(notice); updateState(); });
document.querySelector("#session-retry").addEventListener("click", establishSession);
form.addEventListener("submit", async event => {
  event.preventDefault();
  if (button.disabled || submitting) return;
  const choices = [...form.querySelectorAll("input:checked")].map(input => Number(input.value));
  submitting = true;
  button.textContent = "Отправляем…";
  updateState();
  message(notice, "", "info");
  try {
    let result;
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        result = await request(`/api/polls/${encodeURIComponent(poll.id)}/votes`, {method: "POST", body: {choices}});
        break;
      } catch (error) {
        if (error.status !== 503 || attempt === 2) throw error;
        message(notice, "Сервис занят. Повторяем отправку вашего голоса…", "info");
        await new Promise(resolve => setTimeout(resolve, error.retryAfter * 1000 + Math.random() * 500));
      }
    }
    voted = true;
    form.hidden = true;
    document.querySelector("#poll-instruction").hidden = true;
    document.querySelector("#vote-success").hidden = false;
    if (result.status === 200) message(notice, "Вы уже голосовали в этом опросе. Ваш первый ответ сохранён.", "info");
    else message(notice);
  } catch (error) { message(notice, error.message); }
  finally { submitting = false; button.textContent = "Проголосовать ↗"; updateState(); }
});

initialize();
