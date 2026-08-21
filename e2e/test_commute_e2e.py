#!/usr/bin/env python3

import os
import sys
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright


BASE_URL = os.environ.get("MIND_THE_TIME_BASE_URL", "http://127.0.0.1:5436")
# The strict-replay fixtures are recorded for one origin/destination pair; set
# MTT_E2E_ORIGIN and MTT_E2E_DESTINATION to the addresses your fixtures were
# recorded with (e.g. in the .env loaded by the Taskfile).
ORIGIN = os.environ.get("MTT_E2E_ORIGIN", "10 Rue de Rivoli, 75004 Paris")
DESTINATION = os.environ.get("MTT_E2E_DESTINATION", "Cergy-le-Haut")
WEEKDAYS = ["monday", "tuesday", "wednesday", "thursday", "friday"]


def matching_checkbox(page, name, prefix):
    for checkbox in page.locator(f'input[name="{name}"][id^="{prefix}-"]').all():
        label = page.locator(f'label[for="{checkbox.get_attribute("id")}"]')
        text = label.text_content() or ""
        if "Métro 3" in text and "Transilien L" in text:
            return checkbox
    raise AssertionError("The Métro 3 → Transilien L combination was not available")


def local_request_guard(context):
    base = urlparse(BASE_URL)
    blocked_requests = []

    def guard(route):
        request = urlparse(route.request.url)
        if request.scheme == base.scheme and request.netloc == base.netloc:
            route.continue_()
            return
        blocked_requests.append(route.request.url)
        route.abort()

    context.route("**/*", guard)
    return blocked_requests


def assert_no_external_requests(blocked_requests):
    assert not blocked_requests, (
        "Blocked non-local browser request(s): " + ", ".join(blocked_requests)
    )


def run_e2e_test():
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 390, "height": 844})
        blocked_requests = local_request_guard(context)
        page = context.new_page()

        page.goto(BASE_URL)
        page.locator("#name").fill("Trajet E2E")
        page.locator("#origin").fill(ORIGIN)
        page.locator("#destination").fill(DESTINATION)
        page.wait_for_timeout(1000)
        assert_no_external_requests(blocked_requests)
        page.locator("#departure_time").fill("18:00")
        page.locator("#maximum_initial_walk").fill("5")
        page.locator("#saint_lazare_transfer").fill("7")
        for weekday in WEEKDAYS:
            page.locator(f'label[for="day-{weekday}"]').click()
        page.locator('button[type="submit"]').click()
        page.wait_for_load_state("load")
        assert "/commutes/" in page.url, f"Expected a saved commute URL, got {page.url}"

        page.locator("#explore-date").fill("2026-08-11")
        page.locator("#explore-start").fill("18:00")
        page.locator("#explore-end").fill("18:20")
        page.get_by_role("button", name="LANCER L'EXPLORATION").click()
        page.wait_for_load_state("load")
        exploration = page.locator("#exploration-section")
        assert exploration.is_visible(), "Discovery results should be visible"
        assert page.locator('input[name="fingerprints"]').count() > 0, (
            "Discovery should return server-verified candidates"
        )

        candidate = matching_checkbox(page, "fingerprints", "fp")
        fingerprint = candidate.get_attribute("value")
        candidate.check()
        page.locator('form[action$="/save-combinations"] button[type="submit"]').click()
        page.wait_for_load_state("load")
        planning = page.locator("#planning-section")
        assert planning.is_visible(), "Saved combinations should be available for planning"

        saved_combination = matching_checkbox(page, "saved_combination_ids", "sc")
        saved_combination_id = saved_combination.get_attribute("value")
        assert saved_combination_id and saved_combination_id != fingerprint, (
            "Planning should receive the server-issued opaque saved-combination ID"
        )
        saved_combination.check()
        page.locator("#plan-date").fill("2026-08-12")
        page.locator("#plan-start").fill("18:00")
        page.locator("#plan-end").fill("18:30")
        page.locator('form[action$="/plan"] button[type="submit"]').click()
        page.wait_for_load_state("load")
        assert "Résultats de planification" in (planning.text_content() or ""), (
            "The selected saved combination should have planning results"
        )
        assert page.locator(".departure-option-card .time-value").count() > 0, (
            "The recorded 2026-08-12 timetable should produce a viable departure"
        )
        assert_no_external_requests(blocked_requests)

        context.close()
        browser.close()


if __name__ == "__main__":
    try:
        run_e2e_test()
    except Exception as error:
        print(f"E2E TEST FAILED: {error}")
        sys.exit(1)
