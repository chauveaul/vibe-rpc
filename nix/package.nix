{
  lib,
  buildGoModule,
  installShellFiles,
  stdenv,
}:

buildGoModule (finalAttrs: {
  pname = "vibe-rpc";
  # Keep in step with the release tag; the release workflow refuses to
  # publish when they differ.
  version = "2.5.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
    ];
  };

  # Update when go.sum changes: set to lib.fakeHash, run `nix build`, and
  # copy the hash from the error.
  vendorHash = "sha256-AeWNe4y7EeBLuPV1tf1uXsbI+/8qytj7TO8K6rOq8wc=";

  subPackages = [ "cmd/vibe-rpc" ];

  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X github.com/chauveaul/vibe-rpc/internal/paths.Version=${finalAttrs.version}"
  ];

  nativeBuildInputs = [ installShellFiles ];

  postInstall = lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    installShellCompletion --cmd vibe-rpc \
      --bash <($out/bin/vibe-rpc completion bash) \
      --fish <($out/bin/vibe-rpc completion fish) \
      --zsh <($out/bin/vibe-rpc completion zsh)
  '';

  meta = {
    description = "Discord Rich Presence and local coding stats for AI coding agents";
    homepage = "https://github.com/chauveaul/vibe-rpc";
    changelog = "https://github.com/chauveaul/vibe-rpc/releases/tag/v${finalAttrs.version}";
    license = lib.licenses.mit;
    mainProgram = "vibe-rpc";
    platforms = lib.platforms.unix;
  };
})
