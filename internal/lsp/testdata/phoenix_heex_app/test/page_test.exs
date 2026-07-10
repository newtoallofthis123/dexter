defmodule DexterHeexReview.PageTest do
  use ExUnit.Case, async: true

  test "renders the HEEX edge-case fixture" do
    rendered =
      DexterHeexReview.Page.render(%{
        title: "Review",
        visible?: true,
        items: [%{name: "first"}],
        entries: [%{name: "second"}]
      })

    assert %Phoenix.LiveView.Rendered{} = rendered
    html = rendered |> Phoenix.HTML.Safe.to_iodata() |> IO.iodata_to_binary()
    assert html =~ "Review"
    assert html =~ "first"
    assert html =~ "second"
    assert html =~ "#"
    assert html =~ "unicode"
  end
end
