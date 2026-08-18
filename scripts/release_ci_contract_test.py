#!/usr/bin/env python3

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SETUP_GO_REVISION = "924ae3a1cded613372ab5595356fb5720e22ba16"


def read_repo_file(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


class ReleaseCIContractTest(unittest.TestCase):
    def test_setup_go_uses_one_resolvable_pinned_revision(self) -> None:
        pins: list[tuple[str, str]] = []
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            for pin in re.findall(r"actions/setup-go@([0-9a-f]+)", workflow.read_text()):
                pins.append((workflow.name, pin))

        self.assertTrue(pins, "no actions/setup-go pins found")
        self.assertEqual(
            [],
            [(name, pin) for name, pin in pins if pin != SETUP_GO_REVISION],
        )

    def test_web_manifest_check_runs_from_web_build_directory(self) -> None:
        workflow = read_repo_file(".github/workflows/ci.yml")
        step = re.search(
            r"- name: Onboarding manifest in build output\n(?P<body>(?: {8,}.*\n)+)",
            workflow,
        )

        self.assertIsNotNone(step, "web manifest check step is missing")
        self.assertIn("working-directory: web", step.group("body"))

    def test_test_upstream_has_an_executable_self_probe(self) -> None:
        compose = read_repo_file("docker-compose.test.yml")
        upstream = compose.split("\n  upstream:\n", 1)[1].split("\n  gateway-a:\n", 1)[0]

        self.assertIn("image: python:3.12-alpine", upstream)
        self.assertIn("./tests/upstream_server.py", upstream)
        self.assertIn("urllib.request.urlopen", upstream)
        self.assertNotIn("wget", upstream)

    def test_gateways_share_one_built_test_image(self) -> None:
        compose = read_repo_file("docker-compose.test.yml")
        gateway_a = compose.split("\n  gateway-a:\n", 1)[1].split(
            "\n  gateway-b:\n", 1
        )[0]
        gateway_b = compose.split("\n  gateway-b:\n", 1)[1]

        expected_image = "image: llmrouter-test-gateway:local"
        self.assertIn(expected_image, gateway_a)
        self.assertIn(expected_image, gateway_b)
        self.assertIn("build: .", gateway_a)
        self.assertNotIn("build: .", gateway_b)

    def test_nightly_runs_docker_matrix_before_installing_chromium(self) -> None:
        workflow = read_repo_file(".github/workflows/nightly.yml")

        self.assertLess(
            workflow.index("bash scripts/run-integration-matrix.sh --nightly"),
            workflow.index("bunx playwright install --with-deps chromium"),
        )

    def test_release_certification_runs_docker_matrix_before_chromium(self) -> None:
        workflow = read_repo_file(".github/workflows/release.yml")
        certification = workflow.split("\n  certification:\n", 1)[1].split(
            "\n  release-gate:\n", 1
        )[0]

        self.assertLess(
            certification.index("bash scripts/run-integration-matrix.sh --nightly"),
            certification.index("bunx playwright install --with-deps chromium"),
        )


if __name__ == "__main__":
    unittest.main()
