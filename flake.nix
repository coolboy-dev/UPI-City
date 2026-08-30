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
        # Go and a task runner. That is the entire toolchain.
        #
        # The plan called for Next.js and matplotlib; neither survived contact
        # with the work. The dashboard is hand-written HTML embedded in the
        # binary, and the figures are SVG generated in Go — three chart types
        # did not justify a scientific-Python dependency tree on a
        # memory-constrained machine, or a second toolchain in the demo path.
        #
        # What is left is a shell that opens in seconds and a binary with zero
        # external Go dependencies.
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.just ];

          # net/http otherwise wants a C toolchain for DNS resolution; a
          # pure-Go build is what makes the demo a single portable file.
          CGO_ENABLED = "0";
        };
      });
    };
}
