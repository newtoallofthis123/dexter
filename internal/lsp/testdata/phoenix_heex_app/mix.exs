defmodule DexterHeexReview.MixProject do
  use Mix.Project

  def project do
    [
      app: :dexter_heex_review,
      version: "0.1.0",
      elixir: "~> 1.18",
      deps: [{:phoenix_live_view, "~> 1.1"}]
    ]
  end

  def application, do: [extra_applications: [:logger]]
end
