const axisLockDistance = 10;
const edgeDeadZone = 20;
const pullResistance = 0.5;
const refreshOffset = 64;
const maximumOffset = 96;

export function trackVerticalDrag(element, { canStart, onMove, onRelease, onCancel }) {
  let dragState = "idle";
  let startX = 0;
  let startY = 0;
  let lastDistance = 0;

  element.addEventListener("touchstart", (touchEvent) => {
    if (touchEvent.touches.length !== 1) {
      if (dragState === "dragging") {
        onCancel();
      }
      dragState = "idle";
      return;
    }
    if (!canStart(touchEvent)) {
      dragState = "idle";
      return;
    }
    startX = touchEvent.touches[0].clientX;
    startY = touchEvent.touches[0].clientY;
    lastDistance = 0;
    dragState = "deciding";
  }, { passive: true });

  element.addEventListener("touchmove", (touchEvent) => {
    if (dragState === "idle") {
      return;
    }
    const horizontalTravel = touchEvent.touches[0].clientX - startX;
    const verticalTravel = touchEvent.touches[0].clientY - startY;
    if (dragState === "deciding") {
      if (verticalTravel <= 0 || !touchEvent.cancelable) {
        dragState = "idle";
        return;
      }
      touchEvent.preventDefault();
      if (Math.hypot(horizontalTravel, verticalTravel) < axisLockDistance) {
        return;
      }
      if (verticalTravel <= Math.abs(horizontalTravel)) {
        dragState = "idle";
        return;
      }
      dragState = "dragging";
    }
    if (touchEvent.cancelable) {
      touchEvent.preventDefault();
    }
    lastDistance = Math.max(verticalTravel, 0);
    onMove(lastDistance);
  }, { passive: false });

  element.addEventListener("touchend", () => {
    if (dragState === "dragging") {
      onRelease(lastDistance);
    }
    dragState = "idle";
  });

  element.addEventListener("touchcancel", () => {
    if (dragState === "dragging") {
      onCancel();
    }
    dragState = "idle";
  });
}

export function trackHorizontalSwipe(element, { canStart, onMove, onRelease, onCancel }) {
  let swipeState = "idle";
  let activePointerId = 0;
  let startX = 0;
  let startY = 0;
  let lastDistance = 0;

  element.addEventListener("pointerdown", (pointerEvent) => {
    if (pointerEvent.pointerType !== "touch" || !pointerEvent.isPrimary
      || pointerEvent.clientX < edgeDeadZone || !canStart(pointerEvent)) {
      return;
    }
    activePointerId = pointerEvent.pointerId;
    startX = pointerEvent.clientX;
    startY = pointerEvent.clientY;
    lastDistance = 0;
    swipeState = "deciding";
  });

  element.addEventListener("pointermove", (pointerEvent) => {
    if (swipeState === "idle" || pointerEvent.pointerId !== activePointerId) {
      return;
    }
    const horizontalTravel = pointerEvent.clientX - startX;
    const verticalTravel = pointerEvent.clientY - startY;
    if (swipeState === "deciding") {
      if (Math.hypot(horizontalTravel, verticalTravel) < axisLockDistance) {
        return;
      }
      if (Math.abs(horizontalTravel) <= Math.abs(verticalTravel)) {
        swipeState = "idle";
        return;
      }
      swipeState = "dragging";
    }
    lastDistance = horizontalTravel;
    onMove(lastDistance);
  });

  element.addEventListener("pointerup", (pointerEvent) => {
    if (swipeState === "dragging" && pointerEvent.pointerId === activePointerId) {
      onRelease(lastDistance);
    }
    swipeState = "idle";
  });

  element.addEventListener("pointercancel", () => {
    if (swipeState === "dragging") {
      onCancel();
    }
    swipeState = "idle";
  });
}

export function attachPullToRefresh(onRefresh, excludedElements = []) {
  document.documentElement.classList.add("pull-to-refresh");
  const indicator = document.createElement("div");
  indicator.className = "pull-indicator";
  indicator.setAttribute("aria-hidden", "true");
  document.body.append(indicator);
  let refreshing = false;

  function showOffset(offset) {
    indicator.style.setProperty("--pull-offset", `${offset}px`);
    indicator.style.setProperty("--pull-progress", String(Math.min(offset / refreshOffset, 1)));
    indicator.classList.toggle("armed", offset >= refreshOffset);
  }

  trackVerticalDrag(document, {
    canStart: (touchEvent) => !refreshing
      && window.scrollY <= 0
      && document.querySelector("dialog[open]") === null
      && !excludedElements.some((excludedElement) => excludedElement.contains(touchEvent.target)),
    onMove: (distance) => {
      indicator.classList.add("dragging");
      showOffset(Math.min(distance * pullResistance, maximumOffset));
    },
    onRelease: (distance) => {
      indicator.classList.remove("dragging");
      if (distance * pullResistance < refreshOffset) {
        showOffset(0);
        return;
      }
      refreshing = true;
      indicator.classList.add("refreshing");
      showOffset(refreshOffset);
      onRefresh().finally(() => {
        refreshing = false;
        indicator.classList.remove("refreshing");
        showOffset(0);
      });
    },
    onCancel: () => {
      indicator.classList.remove("dragging");
      showOffset(0);
    },
  });
}
