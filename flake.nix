{
  description = "UPI City — payment network simulation & fraud detection testbed";

  inputs.nixpkgs.url = "flake:nixpkgs";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (s: f nixpkgs.legacyPackages.${s});
    in
    {
      devShells = forAll (pkgs: {
        # Go, a task runner, and Python for the detectors under test.
        #
        # The plan called for Next.js and matplotlib; neither survived contact
        # with the work. The dashboard is hand-written HTML embedded in the
        # binary, and the figures are SVG generated in Go — three chart types
        # did not justify a scientific-Python dependency tree on a
        # memory-constrained machine, or a second toolchain in the demo path.
        #
        # scikit-learn is here for a different reason, and the distinction is
        # the whole point of the testbed: it is not a dependency of UPI City,
        # it is a COMPETITOR that UPI City grades. The platform talks to it
        # through two JSONL files and never links against it. `just build`
        # still produces a single Go binary with zero external dependencies,
        # and deleting this line would cost the project nothing but one
        # entrant on the leaderboard.
        #
        # It comes from nixpkgs rather than pip because pip's manylinux wheels
        # cannot find libstdc++ on NixOS — numpy fails to import, and the
        # failure looks like a broken detector rather than a broken install.
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.just
            # scikit-learn is the entrant itself; numba and scipy are here so
            # PyOD — the open-source anomaly-detection zoo, which nixpkgs does
            # not carry — can be dropped into a --system-site-packages venv
            # with `pip install --no-deps pyod`. PyOD is pure Python, so with
            # its compiled dependencies supplied by Nix there is nothing left
            # for pip to build and the manylinux problem does not arise.
            (pkgs.python3.withPackages (ps: [
              ps.scikit-learn
              ps.numba
              ps.scipy
              ps.networkx
            ]))
          ];

          # net/http otherwise wants a C toolchain for DNS resolution; a
          # pure-Go build is what makes the demo a single portable file.
          CGO_ENABLED = "0";
        };
      });
    };
}
