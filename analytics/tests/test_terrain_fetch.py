"""fetch_glo30 against a fake HTTP layer: concurrent fetches and failures leave no .part files."""

import io
import threading
import urllib.error
import urllib.request
from email.message import Message
from pathlib import Path

import pytest

from meshtwin_analytics import terrain

TILE = terrain.tile_name(47, -123)
PAYLOAD = b"GLO30" * 100_000  # 500 kB, copied in several chunks


def test_concurrent_fetches_install_one_whole_tile(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    both_started = threading.Barrier(2, timeout=10)

    def urlopen(url: str, timeout: float) -> io.BytesIO:
        both_started.wait()  # both jobs are downloading the same tile at the same time
        return io.BytesIO(PAYLOAD)

    monkeypatch.setattr(urllib.request, "urlopen", urlopen)
    reports: list[dict[str, list[str]]] = []
    errors: list[BaseException] = []

    def fetch() -> None:
        try:
            reports.append(terrain.fetch_glo30(tmp_path, 47.5, -122.5, 47.5, -122.5))
        except BaseException as e:  # surfaced below; a thread would swallow it
            errors.append(e)

    threads = [threading.Thread(target=fetch) for _ in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert errors == []
    assert [r["downloaded"] for r in reports] == [[TILE], [TILE]]
    assert [p.name for p in tmp_path.iterdir()] == [f"{TILE}.tif"]
    assert (tmp_path / f"{TILE}.tif").read_bytes() == PAYLOAD


class _BrokenStream(io.BytesIO):
    def read(self, size: int | None = -1) -> bytes:
        raise ConnectionResetError("connection dropped mid-download")


def test_failed_download_leaves_nothing(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(urllib.request, "urlopen", lambda url, timeout: _BrokenStream())
    with pytest.raises(ConnectionResetError):
        terrain.fetch_glo30(tmp_path, 47.5, -122.5, 47.5, -122.5)
    assert list(tmp_path.iterdir()) == []


def _http_error(code: int) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("u", code, "x", Message(), None)


def test_404_is_no_tile_and_500_raises(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    def not_found(url: str, timeout: float) -> io.BytesIO:
        raise _http_error(404)

    monkeypatch.setattr(urllib.request, "urlopen", not_found)
    sea = tmp_path / "sea"
    report = terrain.fetch_glo30(sea, 47.5, -122.5, 47.5, -122.5)
    assert report["no_tile"] == [TILE]
    # The 404 is remembered: open sea, not "never fetched", and not asked for again.
    assert [f.name for f in sea.iterdir()] == [f"{TILE}.notile"]
    assert terrain.missing_tiles(sea, 47.5, -122.5, 47.5, -122.5) == []

    def server_error(url: str, timeout: float) -> io.BytesIO:
        raise _http_error(500)

    monkeypatch.setattr(urllib.request, "urlopen", server_error)
    assert terrain.fetch_glo30(sea, 47.5, -122.5, 47.5, -122.5)["no_tile"] == [TILE]

    failed = tmp_path / "failed"
    with pytest.raises(urllib.error.HTTPError):
        terrain.fetch_glo30(failed, 47.5, -122.5, 47.5, -122.5)
    assert list(failed.iterdir()) == []
    assert terrain.missing_tiles(failed, 47.5, -122.5, 47.5, -122.5) == [TILE]


def test_present_tile_is_not_fetched(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    (tmp_path / f"{TILE}.tif").write_bytes(b"cached")

    def must_not_call(url: str, timeout: float) -> io.BytesIO:
        raise AssertionError("fetched a tile that was already cached")

    monkeypatch.setattr(urllib.request, "urlopen", must_not_call)
    report = terrain.fetch_glo30(tmp_path, 47.5, -122.5, 47.5, -122.5)
    assert report["present"] == [TILE]
