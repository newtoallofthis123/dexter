defmodule HeexApp.ScopeLive do
  use Phoenix.Component

  alias HeexApp.Components

  def render(assigns) do
    ~H"""
    <%= for item <- assigns.items do %>
      {item.name}
    <% end %>
    {item}

    <%= case assigns.result do %>
      <% {:ok, clause_item} -> %>
        {clause_item.name}
      <% {:error, clause_item} -> %>
        {clause_item.message}
    <% end %>
    {clause_item}

    <li :for={entry <- assigns.entries}>{entry.name}</li>
    {entry}

    <Components.wrapper :let={slot_item}>{slot_item}</Components.wrapper>
    {slot_item}
    """
  end
end
