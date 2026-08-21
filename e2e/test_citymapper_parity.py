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


def fingerprint_labels(page):
    labels = []
    for fingerprint in page.locator('input[name="fingerprints"]').all():
        label = page.locator(f'label[for="{fingerprint.get_attribute("id")}"]')
        labels.append((label.text_content() or "").strip())
    return labels


def run_fixture_replay_test():
    print("Running strict-replay discovery fixture E2E test...")

    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 390, "height": 844})
        blocked_requests = local_request_guard(context)
        page = context.new_page()

        page.goto(BASE_URL)
        page.locator("#name").fill("Trajet parité fixtures")
        page.locator("#origin").fill(ORIGIN)
        page.locator("#destination").fill(DESTINATION)
        page.locator("#departure_time").fill("18:00")
        page.locator("#maximum_initial_walk").fill("5")
        page.locator("#saint_lazare_transfer").fill("7")
        for weekday in WEEKDAYS:
            page.locator(f'label[for="day-{weekday}"]').click()
        page.wait_for_timeout(1000)
        assert_no_external_requests(blocked_requests)

        page.locator('button[type="submit"]').click()
        page.wait_for_load_state("load")
        assert "/commutes/" in page.url, f"Expected a saved commute URL, got {page.url}"

        page.locator("#explore-date").fill("2026-08-11")
        page.locator("#explore-start").fill("18:00")
        page.locator("#explore-end").fill("18:20")
        page.get_by_role("button", name="LANCER L'EXPLORATION").click()
        page.wait_for_load_state("load")

        exploration = page.locator("#exploration-section")
        assert exploration.is_visible(), "Fixture discovery results should be visible"
        labels = fingerprint_labels(page)
        assert labels, "Fixture discovery should return candidate line combinations"
        expected_combination = next(
            (label for label in labels if "Métro 3" in label and "Transilien L" in label),
            None,
        )
        assert expected_combination, (
            "Expected Métro 3 → Transilien L fixture candidate; found: " + ", ".join(labels)
        )
        print(f"Confirmed fixture candidate: {expected_combination}")
        assert_no_external_requests(blocked_requests)

        context.close()
        browser.close()

if __name__ == "__main__":
    try:
        run_fixture_replay_test()
    except Exception as e:
        print(f"\nE2E FIXTURE REPLAY TEST FAILED: {e}")
        sys.exit(1)
