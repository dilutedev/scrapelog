import { apiFetch } from "./shell.js";

const facetFieldPattern = /^(-?)(domain|scraper|project|proxy|method|tls):"?([^"\s]*)$/;
const autocompleteDebounceMilliseconds = 150;

export function canFilter(fieldName, value) {
  if (!value || value.includes('"')) {
    return false;
  }
  return !/[:~"\s]/.test(fieldName);
}

export function formatQueryTerm(fieldName, value, excluded) {
  const formattedValue = /\s/.test(value) ? `"${value}"` : value;
  return (excluded ? "-" : "") + fieldName + ":" + formattedValue;
}

function splitQueryTokens(queryText) {
  const tokens = [];
  let currentToken = "";
  let insideQuotes = false;
  for (const character of queryText) {
    if (character === '"') {
      insideQuotes = !insideQuotes;
      currentToken += character;
    } else if (/\s/.test(character) && !insideQuotes) {
      if (currentToken) {
        tokens.push(currentToken);
        currentToken = "";
      }
    } else {
      currentToken += character;
    }
  }
  if (currentToken) {
    tokens.push(currentToken);
  }
  return tokens;
}

export function addQueryTerm(queryText, term) {
  const tokens = splitQueryTokens(queryText);
  if (tokens.includes(term)) {
    return tokens.join(" ");
  }
  const oppositeTerm = term.startsWith("-") ? term.slice(1) : "-" + term;
  const oppositeIndex = tokens.indexOf(oppositeTerm);
  if (oppositeIndex !== -1) {
    tokens[oppositeIndex] = term;
    return tokens.join(" ");
  }
  tokens.push(term);
  return tokens.join(" ");
}

export function attachFacetAutocomplete(inputElement, listElement, currentRange, currentSourceParameters) {
  let debounceTimer = null;
  let requestCounter = 0;
  let activeIndex = -1;
  let activeToken = null;

  function closeList() {
    requestCounter += 1;
    clearTimeout(debounceTimer);
    listElement.hidden = true;
    listElement.replaceChildren();
    activeIndex = -1;
    inputElement.setAttribute("aria-expanded", "false");
    inputElement.removeAttribute("aria-activedescendant");
  }

  function tokenAtCaret() {
    const caretPosition = inputElement.selectionStart;
    const textBeforeCaret = inputElement.value.slice(0, caretPosition);
    let tokenStart = 0;
    let insideQuotes = false;
    for (let index = 0; index < textBeforeCaret.length; index += 1) {
      const character = textBeforeCaret[index];
      if (character === '"') {
        insideQuotes = !insideQuotes;
      } else if (/\s/.test(character) && !insideQuotes) {
        tokenStart = index + 1;
      }
    }
    const tokenText = textBeforeCaret.slice(tokenStart);
    const match = facetFieldPattern.exec(tokenText);
    if (!match) {
      return null;
    }
    return { tokenStart, caretPosition, negated: match[1] === "-", fieldName: match[2], prefix: match[3] };
  }

  function setActiveIndex(newIndex) {
    const options = listElement.children;
    for (const option of options) {
      option.setAttribute("aria-selected", "false");
    }
    activeIndex = newIndex;
    if (newIndex >= 0) {
      options[newIndex].setAttribute("aria-selected", "true");
      inputElement.setAttribute("aria-activedescendant", options[newIndex].id);
    } else {
      inputElement.removeAttribute("aria-activedescendant");
    }
  }

  function acceptOption(optionIndex) {
    const option = listElement.children[optionIndex];
    const replacement = formatQueryTerm(activeToken.fieldName, option.dataset.value, activeToken.negated) + " ";
    const originalText = inputElement.value;
    inputElement.value = originalText.slice(0, activeToken.tokenStart) + replacement + originalText.slice(activeToken.caretPosition);
    const caretPosition = activeToken.tokenStart + replacement.length;
    inputElement.setSelectionRange(caretPosition, caretPosition);
    closeList();
  }

  function renderOptions(facetValues) {
    listElement.replaceChildren();
    facetValues.forEach((facetValue, index) => {
      const option = document.createElement("li");
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", "false");
      option.id = "suggestion-" + index;
      option.dataset.value = facetValue.value;
      option.append(facetValue.value);
      const countLabel = document.createElement("span");
      countLabel.className = "suggestion-count";
      countLabel.textContent = String(facetValue.count);
      option.append(countLabel);
      listElement.append(option);
    });
    activeIndex = -1;
    listElement.hidden = false;
    inputElement.setAttribute("aria-expanded", "true");
  }

  async function loadSuggestions(token) {
    requestCounter += 1;
    const thisRequest = requestCounter;
    const { from, to } = currentRange();
    const facetField = token.fieldName === "tls" ? "tls_profile" : token.fieldName;
    try {
      const facetResponse = await apiFetch("/ui/api/facets?" + new URLSearchParams({ field: facetField, prefix: token.prefix, from: from.toISOString(), to: to.toISOString(), ...currentSourceParameters() }));
      if (thisRequest !== requestCounter) {
        return;
      }
      if (!facetResponse.values || facetResponse.values.length === 0) {
        closeList();
        return;
      }
      activeToken = token;
      renderOptions(facetResponse.values);
    } catch {
      if (thisRequest === requestCounter) {
        closeList();
      }
    }
  }

  inputElement.addEventListener("input", () => {
    closeList();
    const token = tokenAtCaret();
    if (!token) {
      closeList();
      return;
    }
    debounceTimer = setTimeout(() => loadSuggestions(token), autocompleteDebounceMilliseconds);
  });

  inputElement.addEventListener("keydown", (keyEvent) => {
    if (listElement.hidden) {
      return;
    }
    const optionCount = listElement.children.length;
    if (keyEvent.key === "ArrowDown") {
      keyEvent.preventDefault();
      setActiveIndex((activeIndex + 1) % optionCount);
    } else if (keyEvent.key === "ArrowUp") {
      keyEvent.preventDefault();
      setActiveIndex((activeIndex - 1 + optionCount) % optionCount);
    } else if ((keyEvent.key === "Enter" || keyEvent.key === "Tab") && activeIndex >= 0) {
      keyEvent.preventDefault();
      acceptOption(activeIndex);
    } else if (keyEvent.key === "Escape") {
      closeList();
    }
  });

  listElement.addEventListener("pointerdown", (pointerEvent) => {
    const option = pointerEvent.target.closest("li");
    if (!option) {
      return;
    }
    pointerEvent.preventDefault();
    acceptOption(Array.prototype.indexOf.call(listElement.children, option));
  });

  inputElement.addEventListener("blur", closeList);
}
